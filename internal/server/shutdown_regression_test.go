package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/stats"
)

func TestShutdownDrainsRequestsWithOpenLogSubscription(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "providers.yaml")
	srv := New(&config.Config{Providers: []config.Provider{{Name: "test", ModelID: "mock", APIKey: "test", BaseURL: upstream.URL, Format: "openai", Priority: 1, Timeout: 1}}}, path)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Handler: srv}
	go httpSrv.Serve(listener)
	base := "http://" + listener.Addr().String()
	logs, err := http.Get(base + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Body.Close()
	done := make(chan error, 1)
	go func() {
		resp, err := http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{"model":"P1","messages":[]}`))
		if err == nil {
			_, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				err = io.ErrUnexpectedEOF
			}
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for srv.statsCollector.Snapshot().TotalClientReq == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx, httpSrv); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if snap := stats.New(statsFilePath(path)).Snapshot(); snap.TotalClientSuccess != 1 || snap.TotalClientCompleted != 1 {
		t.Fatalf("lost drained outcome: %+v", snap)
	}
}

func TestStatsCacheInvalidatesOnControlAndReset(t *testing.T) {
	srv := New(&config.Config{Providers: []config.Provider{{Name: "test", ModelID: "mock", Format: "openai", Priority: 1}}}, "")
	defer srv.SaveStats()
	get := func() string {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))
		return w.Body.String()
	}
	if !strings.Contains(get(), `"running":true`) {
		t.Fatal("not running")
	}
	srv.statsCollector.SetRunning(false)
	if !strings.Contains(get(), `"running":false`) {
		t.Fatal("cached running state")
	}
	srv.statsCollector.RecordClientRequest("one")
	_ = get()
	srv.statsCollector.Reset()
	if !strings.Contains(get(), `"total_client_req":0`) {
		t.Fatal("cached counters after reset")
	}
}

func TestShutdownTimeoutStillPersistsFinalOutcomes(t *testing.T) {
	entered := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "providers.yaml")
	srv := New(&config.Config{Providers: []config.Provider{{Name: "test", ModelID: "mock", APIKey: "test", BaseURL: upstream.URL, Format: "openai", Priority: 1, Timeout: 60}}}, path)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Handler: srv}
	go httpSrv.Serve(listener)
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, _ := http.Post("http://"+listener.Addr().String()+"/chat/completions", "application/json", strings.NewReader(`{"model":"P1"}`))
		if resp != nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request not forwarded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := srv.Shutdown(ctx, httpSrv); err == nil {
		t.Fatal("wanted drain timeout")
	}
	<-done
	if snap := stats.New(statsFilePath(path)).Snapshot(); snap.TotalClientCompleted != 1 || snap.TotalClientReq != 1 {
		t.Fatalf("forced shutdown lost outcome: %+v", snap)
	}
}
