package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy/internal/config"
)

// The pre-stop gate (stop-cli / deploy-rpi) polls /api/inflight and waits for
// zero before stopping the process. The gauge must therefore count exactly
// the forwarding routes: dashboard traffic, including a long-lived SSE log
// subscription, must never move it off zero or the gate would wait forever.
func TestInflightCountsForwardingOnly(t *testing.T) {
	block := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer upstream.Close()
	path := filepath.Join(t.TempDir(), "providers.yaml")
	srv := New(&config.Config{Providers: []config.Provider{{Name: "test", ModelID: "mock", APIKey: "test", BaseURL: upstream.URL, Format: "openai", Priority: 1, Timeout: 5}}}, path)
	defer srv.SaveStats()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := &http.Server{Handler: srv}
	defer httpSrv.Close()
	go httpSrv.Serve(listener)
	base := "http://" + listener.Addr().String()

	count := func() int64 {
		resp, err := http.Get(base + "/api/inflight")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var got struct {
			Forwarding int64 `json:"forwarding"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		return got.Forwarding
	}

	// Baseline: an open SSE log subscription plus stats polling must not
	// register as forwarding.
	logs, err := http.Get(base + "/api/logs")
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Body.Close()
	if _, err := http.Get(base + "/api/stats"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Fatalf("dashboard traffic counted as forwarding: %d", n)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.Post(base+"/chat/completions", "application/json", strings.NewReader(`{"model":"P1","messages":[]}`))
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	deadline := time.Now().Add(2 * time.Second)
	for count() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if n := count(); n != 1 {
		t.Fatalf("in-flight forwarding request not counted: %d", n)
	}

	close(block)
	<-done
	if n := count(); n != 0 {
		t.Fatalf("gauge did not return to zero: %d", n)
	}
}
