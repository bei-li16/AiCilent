package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/stream"
	"ai-proxy/internal/tracer"
	"github.com/gin-gonic/gin"
)

func TestLargeResponsesCompletionRetainsTerminal(t *testing.T) {
	var src bytes.Buffer
	for i := 0; i < 24; i++ {
		b, _ := json.Marshal(map[string]interface{}{"id": "long", "choices": []interface{}{map[string]interface{}{"index": 0, "delta": map[string]string{"content": strings.Repeat("x", 32768)}}}})
		src.WriteString("data: " + string(b) + "\n\n")
	}
	src.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	w := newTerminalWriter(io.Discard, "responses")
	w.framer.MaxBytes = stream.MaxGeneratedEventBytes
	err := adapter.StreamConvertResponse(&src, w, "openai", "responses")
	if err != errStreamTerminal || !w.Terminated() {
		t.Fatalf("terminated=%v err=%v", w.Terminated(), err)
	}
}

func TestPreparationPreservesNumbersAndCachesAliases(t *testing.T) {
	body := []byte(`{"model":"P1","messages":[{"role":"developer","content":"test","extra":9007199254740993}],"unknown":{"n":9007199254740993},"reasoning_effort":"none"}`)
	p := &config.Provider{ModelID: "glm-5.3", Format: "openai", BaseURL: "https://token.sensenova.cn"}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(preparationCacheKey, make(map[preparationKey]preparedBody))
	one := cachedPreparation(c, normalizeDeveloperRole(body), "openai", p, "")
	p.Name = "another-account"
	two := cachedPreparation(c, body, "openai", p, "")
	if one.err != nil || &one.body[0] != &two.body[0] || !bytes.Contains(one.body, []byte("9007199254740993")) || !bytes.Contains(one.body, []byte(`"role":"system"`)) || bytes.Contains(one.body, []byte("reasoning_effort")) {
		t.Fatalf("bad preparation: %s", one.body)
	}
}

func TestIdleReaderTimeoutAndBackpressure(t *testing.T) {
	r, w := io.Pipe()
	reader := newIdleTimeoutReader(r, 20*time.Millisecond)
	defer reader.Close()
	defer w.Close()
	_, err := reader.Read(make([]byte, 8))
	if !isIdleTimeoutErr(err) {
		t.Fatalf("want idle timeout, got %v", err)
	}
	r2, w2 := io.Pipe()
	reader2 := newIdleTimeoutReader(r2, 30*time.Millisecond)
	defer reader2.Close()
	defer w2.Close()
	go func() { _, _ = w2.Write([]byte("a")); time.Sleep(80 * time.Millisecond); _, _ = w2.Write([]byte("b")) }()
	buf := make([]byte, 1)
	if _, err = reader2.Read(buf); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err = reader2.Read(buf); err != nil || string(buf) != "b" {
		t.Fatalf("downstream backpressure became upstream idle: %v", err)
	}
}

func TestStreamTransportCacheBoundAndReload(t *testing.T) {
	e := &Engine{transport: &http.Transport{}, streamTransports: make(map[time.Duration]*http.Transport)}
	for i := 0; i < 100; i++ {
		e.streamTransport(i)
	}
	if len(e.streamTransports) > 16 {
		t.Fatal(len(e.streamTransports))
	}
	e.pruneStreamTransports(&config.Config{Providers: []config.Provider{{Timeout: 60}}})
	for timeout := range e.streamTransports {
		if timeout != time.Minute {
			t.Fatal(timeout)
		}
	}
}

func TestWatcherRetriesFailureAndDetectsPreservedMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	valid := []byte("providers:\n  - name: test\n    api_key: test\n    base_url: http://localhost\n    model_id: original\n    format: openai\n    priority: 1\n")
	if err := os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(cfg, path, io.Discard, io.Discard, stats.New(""))
	ctx, cancel := context.WithCancel(context.Background())
	done := e.StartWatcher(ctx, 10*time.Millisecond)
	defer func() { cancel(); <-done }()
	info, _ := os.Stat(path)
	_ = os.WriteFile(path, []byte("invalid: ["), 0600)
	time.Sleep(40 * time.Millisecond)
	e.reloadMu.RLock()
	model := e.cfg.Providers[0].ModelID
	e.reloadMu.RUnlock()
	if model != "original" {
		t.Fatal("invalid config replaced running config")
	}
	_ = os.WriteFile(path, bytes.ReplaceAll(valid, []byte("original"), []byte("newmodel")), 0600)
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		e.reloadMu.RLock()
		model = e.cfg.Providers[0].ModelID
		e.reloadMu.RUnlock()
		if model == "newmodel" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("same-mtime edit was not applied after failure")
}

func BenchmarkPreparedLargeRequest(b *testing.B) {
	body := []byte(`{"model":"P1","messages":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}],"reasoning_effort":"none"}`)
	p := &config.Provider{ModelID: "kimi", Format: "openai", BaseURL: "https://token.sensenova.cn"}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		prepareProviderRequest(body, p, "")
	}
}

func TestUnchangedHealthyBreakerDoesNotWriteState(t *testing.T) {
	e := NewEngine(&config.Config{}, filepath.Join(t.TempDir(), "providers.yaml"), io.Discard, io.Discard, nil)
	tr := tracer.New("P1", "openai", "test", io.Discard, io.Discard)
	e.closeCircuitBreaker(1, "P1", tr, 3, 60, 10)
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(e.cbStatePath(), past, past); err != nil {
		t.Fatal(err)
	}
	e.closeCircuitBreaker(1, "P1", tr, 3, 60, 10)
	info, err := os.Stat(e.cbStatePath())
	if err != nil || !info.ModTime().Equal(past) {
		t.Fatal("unchanged healthy breaker wrote to disk", err)
	}
}
