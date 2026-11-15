// perfprobe compares real binaries against a disposable loopback-only upstream.
// It never loads the production YAML or calls an external inference endpoint.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type usage struct{ RSSKiB, PeakKiB, Threads, Ticks, WriteBytes, WriteCalls int64 }

func sample(pid int) usage {
	var u usage
	data, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			u.RSSKiB = n
		case "VmHWM:":
			u.PeakKiB = n
		case "Threads:":
			u.Threads = n
		}
	}
	data, _ = os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	text := string(data)
	if end := strings.LastIndex(text, ")"); end >= 0 {
		f := strings.Fields(text[end+1:])
		if len(f) > 12 {
			a, _ := strconv.ParseInt(f[11], 10, 64)
			b, _ := strconv.ParseInt(f[12], 10, 64)
			u.Ticks = a + b
		}
	}
	data, _ = os.ReadFile(fmt.Sprintf("/proc/%d/io", pid))
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "write_bytes:":
			u.WriteBytes = n
		case "syscw:":
			u.WriteCalls = n
		}
	}
	return u
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func main() {
	binary := flag.String("binary", "", "binary under test")
	work := flag.String("work", "", "disposable results directory")
	requests := flag.Int("requests", 320, "fixed request count")
	flag.Parse()
	if *binary == "" || *work == "" {
		panic("-binary and -work are required")
	}
	abs, err := filepath.Abs(*binary)
	must(err)
	must(os.MkdirAll(*work, 0755))
	content := strings.Repeat("x", 512)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Stream bool `json:"stream"`
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
		_ = json.Unmarshal(data, &input)
		anthropic := strings.HasSuffix(r.URL.Path, "/messages")
		if !input.Stream {
			w.Header().Set("Content-Type", "application/json")
			if anthropic {
				fmt.Fprintf(w, `{"id":"mock","type":"message","role":"assistant","model":"mock","content":[{"type":"text","text":"%s"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`, content)
			} else {
				fmt.Fprintf(w, `{"id":"mock","choices":[{"index":0,"message":{"role":"assistant","content":"%s"},"finish_reason":"stop"}]}`, content)
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flush := w.(http.Flusher)
		if anthropic {
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"mock\",\"role\":\"assistant\"}}\n\n")
		}
		for i := 0; i < 64; i++ {
			if anthropic {
				fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n", content)
			} else {
				fmt.Fprintf(w, "data: {\"id\":\"mock\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"%s\"}}]}\n\n", content)
			}
			flush.Flush()
			time.Sleep(time.Millisecond)
		}
		if anthropic {
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		} else {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		}
	}))
	defer mock.Close()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	addr := l.Addr().String()
	l.Close()
	base := "http://" + addr
	providers := make([]map[string]interface{}, 0, 90)
	for priority := 1; priority <= 3; priority++ {
		for _, format := range []string{"openai", "anthropic"} {
			for i := 0; i < 15; i++ {
				providers = append(providers, map[string]interface{}{"name": fmt.Sprintf("%s-%d-%d", format, priority, i), "model_id": "mock", "api_key": fmt.Sprintf("fake-%d", i), "base_url": mock.URL + "/v1", "priority": priority, "format": format, "timeout": 60, "retry": map[string]interface{}{"max_retries": 3, "retry_interval": 1, "backoff_factor": 1}})
			}
		}
	}
	cfg := map[string]interface{}{"global": map[string]interface{}{"listen_addr": addr, "log_file": "proxy.log", "log_request_body": "snippet", "max_stream_minutes": 15}, "providers": providers}
	data, _ := json.Marshal(cfg)
	configPath := filepath.Join(*work, "providers.json")
	must(os.WriteFile(configPath, data, 0600))
	output, err := os.Create(filepath.Join(*work, "process.log"))
	must(err)
	cmd := exec.Command(abs, "-config", configPath)
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = append(os.Environ(), "GODEBUG=gctrace=1")
	must(cmd.Start())
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		output.Close()
	}()
	client := &http.Client{Timeout: 30 * time.Second}
	get := func(path string) []byte {
		resp, err := client.Get(base + path)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return b
	}
	for deadline := time.Now().Add(10 * time.Second); len(get("/health")) == 0; {
		if time.Now().After(deadline) {
			panic("child did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	logCtx, cancelLogs := context.WithCancel(context.Background())
	logReq, _ := http.NewRequestWithContext(logCtx, "GET", base+"/api/logs", nil)
	logs, err := http.DefaultClient.Do(logReq)
	must(err)
	logDone := make(chan struct{})
	go func() { defer close(logDone); _, _ = io.Copy(io.Discard, logs.Body) }()
	var payloads [5][]byte
	paths := [5]string{"/chat/completions", "/chat/completions", "/responses", "/v1/messages", "/v1/messages"}
	for i := range payloads {
		body := map[string]interface{}{"model": "P1", "stream": i == 1 || i == 2 || i == 4, "messages": []interface{}{map[string]string{"role": "user", "content": strings.Repeat("a", 128<<10)}}}
		if i == 2 {
			delete(body, "messages")
			body["input"] = strings.Repeat("a", 128<<10)
		}
		payloads[i], _ = json.Marshal(body)
	}
	send := func(i int) (float64, float64, error) {
		start := time.Now()
		resp, err := client.Post(base+paths[i%5], "application/json", bytes.NewReader(payloads[i%5]))
		if err != nil {
			return 0, 0, err
		}
		first := time.Since(start).Seconds() * 1000
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			return 0, 0, fmt.Errorf("status %d: %.100s", resp.StatusCode, b)
		}
		if i%5 == 2 && !bytes.Contains(b, []byte("response.completed")) {
			return 0, 0, fmt.Errorf("missing Responses terminal")
		}
		return time.Since(start).Seconds() * 1000, first, err
	}
	for i := 0; i < 10; i++ {
		_, _, err := send(i)
		must(err)
	}
	time.Sleep(2 * time.Second)
	before := sample(cmd.Process.Pid)
	stopPoll := make(chan struct{})
	var pollWG sync.WaitGroup
	for i := 0; i < 4; i++ {
		pollWG.Add(1)
		go func() {
			defer pollWG.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_ = get("/api/stats")
				case <-stopPoll:
					return
				}
			}
		}()
	}
	stopSample := make(chan struct{})
	sampleDone := make(chan struct{})
	var peakRSS, peakThreads int64
	go func() {
		defer close(sampleDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				u := sample(cmd.Process.Pid)
				if u.RSSKiB > peakRSS {
					peakRSS = u.RSSKiB
				}
				if u.Threads > peakThreads {
					peakThreads = u.Threads
				}
			case <-stopSample:
				return
			}
		}
	}()
	var index, errors atomic.Int64
	var durations, firsts []float64
	var mu sync.Mutex
	var workers sync.WaitGroup
	start := time.Now()
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				n := int(index.Add(1)) - 1
				if n >= *requests {
					return
				}
				duration, first, err := send(n)
				mu.Lock()
				if err != nil {
					errors.Add(1)
					fmt.Fprintln(os.Stderr, err)
				} else {
					durations = append(durations, duration)
					firsts = append(firsts, first)
				}
				mu.Unlock()
			}
		}()
	}
	workers.Wait()
	elapsed := time.Since(start).Seconds()
	close(stopPoll)
	pollWG.Wait()
	close(stopSample)
	<-sampleDone
	after := sample(cmd.Process.Pid)
	stats := json.RawMessage(get("/api/stats"))
	time.Sleep(10 * time.Second)
	idle := sample(cmd.Process.Pid)
	cancelLogs()
	logs.Body.Close()
	<-logDone
	shutdown := time.Now()
	must(cmd.Process.Signal(syscall.SIGTERM))
	err = cmd.Wait()
	waited = true
	must(err)
	output.Close()
	gcLog, _ := os.ReadFile(filepath.Join(*work, "process.log"))
	ticks, _ := exec.Command("getconf", "CLK_TCK").Output()
	hz, _ := strconv.ParseFloat(strings.TrimSpace(string(ticks)), 64)
	if hz == 0 {
		hz = 100
	}
	sort.Float64s(durations)
	sort.Float64s(firsts)
	quantile := func(values []float64, p float64) float64 {
		if len(values) == 0 {
			return 0
		}
		return values[int(float64(len(values)-1)*p)]
	}
	report := map[string]interface{}{"binary": abs, "requests": *requests, "errors": errors.Load(), "elapsed_seconds": elapsed, "cpu_seconds": float64(after.Ticks-before.Ticks) / hz, "requests_per_second": float64(*requests) / elapsed, "p50_ms": quantile(durations, .5), "p95_ms": quantile(durations, .95), "first_response_p95_ms": quantile(firsts, .95), "before": before, "after": after, "idle_after_10s": idle, "sampled_peak_rss_kib": peakRSS, "sampled_peak_threads": peakThreads, "write_bytes": after.WriteBytes - before.WriteBytes, "write_calls": after.WriteCalls - before.WriteCalls, "shutdown_ms": time.Since(shutdown).Seconds() * 1000, "gc_cycles": strings.Count(string(gcLog), "gc "), "stats": stats}
	data, _ = json.MarshalIndent(report, "", "  ")
	must(os.WriteFile(filepath.Join(*work, "report.json"), data, 0644))
	fmt.Println(string(data))
	if errors.Load() != 0 {
		os.Exit(1)
	}
}
