package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/tracer"
	"github.com/gin-gonic/gin"
)

func TestTerminalWriterRequiresBlankLineAndStructuralJSON(t *testing.T) {
	for _, format := range []string{"anthropic", "responses", "openai"} {
		var output strings.Builder
		w := newTerminalWriter(&output, format)
		payload := "data: [DONE]\n"
		if format == "anthropic" {
			payload = "event: message_stop\ndata: {\"type\": \"message_stop\"}\n"
		}
		if format == "responses" {
			payload = "event: response.completed\ndata: {\"type\": \"response.completed\"}\n"
		}
		if _, err := w.Write([]byte(payload)); err != nil || w.Terminated() || output.Len() != 0 {
			t.Fatalf("premature event: %s %v", format, err)
		}
		if _, err := w.Write([]byte("\n")); !errors.Is(err, errStreamTerminal) || output.String() != payload+"\n" {
			t.Fatalf("incomplete terminator: %s %v %q", format, err, output.String())
		}
	}
}

func TestBareCRTerminatorDoesNotWaitForEOF(t *testing.T) {
	var output strings.Builder
	w := newTerminalWriter(&output, "openai")
	if _, err := w.Write([]byte("data: [DONE]\r\r")); !errors.Is(err, errStreamTerminal) || !w.Terminated() {
		t.Fatalf("bare CR terminator waited for another read: %v", err)
	}
}

func TestForwardStreamRejectsEOFAndDoesNotWaitAfterCompleteConversion(t *testing.T) {
	for _, format := range []string{"openai", "responses", "anthropic"} {
		for _, complete := range []bool{false, true} {
			t.Run(format+"/"+map[bool]string{false: "truncated", true: "complete"}[complete], func(t *testing.T) {
				release := make(chan struct{})
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"id\":\"x\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n")
					if complete {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
						w.(http.Flusher).Flush()
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}
				}))
				defer up.Close()
				defer close(release)
				e, p := newStreamEngine(t, up.URL, config.GlobalConfig{})
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest("POST", "/chat/completions", nil)
				err := e.forwardRequestStream(c, []byte(`{"model":"m","stream":true,"messages":[],"input":"x"}`), format, p, 2, tracer.New("m", format, "r", io.Discard, io.Discard))
				if complete && err != nil {
					t.Fatal(err)
				}
				if !complete && err == nil {
					t.Fatal("truncated stream accepted")
				}
				if complete && format == "openai" && !strings.Contains(recorder.Body.String(), `"total_tokens":3`) {
					t.Fatal("usage after finish_reason was lost")
				}
				if !complete && (strings.Contains(recorder.Body.String(), "response.completed") || strings.Contains(recorder.Body.String(), "message_stop")) {
					t.Fatal("truncation was converted into completion")
				}
			})
		}
	}
}

func TestSemanticFinishThenIdleIsStillFailure(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer up.Close()
	defer close(release)
	e, p := newStreamEngine(t, up.URL, config.GlobalConfig{})
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/chat/completions", nil)
	err := e.forwardRequestStream(c, []byte(`{"model":"m","stream":true}`), "openai", p, 1, tracer.New("m", "openai", "r", io.Discard, io.Discard))
	if !isIdleTimeoutErr(err) || strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("idle after finish=%v body=%s", err, recorder.Body.String())
	}
}

type failedResponseWriter struct{ *httptest.ResponseRecorder }

func (w failedResponseWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestDownstreamWriteFailureIsNotProviderFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer up.Close()
	e, _ := newStreamEngine(t, up.URL, config.GlobalConfig{CBThreshold: 1})
	c, _ := gin.CreateTestContext(failedResponseWriter{httptest.NewRecorder()})
	c.Set(middleware.CtxFormatKey, "openai")
	c.Request = httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	e.HandleRequest(c)
	snap := e.stats.Snapshot()
	if snap.TotalCanceled != 1 || snap.TotalFail != 0 || e.failureCount[1] != 0 {
		t.Fatalf("write error blamed on upstream: %+v failures=%d", snap, e.failureCount[1])
	}
}

func TestHalfOpenAdmitsOnlyOneConcurrentProbe(t *testing.T) {
	e := NewEngine(&config.Config{}, "", io.Discard, io.Discard, stats.New(""))
	tr := tracer.New("m", "openai", "r", io.Discard, io.Discard)
	e.recordGroupFailure("m", 1, tr, 1, 60, 0)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !e.shouldSkipGroup(1, "m", tr, 1, 60, 0, false) {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 || e.circuitOpenSince[1] == nil {
		t.Fatalf("admitted=%d; breaker prematurely reset", admitted.Load())
	}
	e.recordGroupFailure("m", 1, tr, 1, 60, 1, e.probeLease[1])
	if time.Since(*e.circuitOpenSince[1]) > time.Second {
		t.Fatal("failed probe did not restart cooldown")
	}
}

func TestCoolingProvidersAreNotForcedOrCountedAsFailures(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	e, _ := newStreamEngine(t, up.URL, config.GlobalConfig{CBThreshold: 1, RateLimitCooldown: 30})
	e.markProviderCooldown("p")
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	c.Set(middleware.CtxFormatKey, "openai")
	e.HandleRequest(c)
	if calls.Load() != 0 || e.failureCount[1] != 0 || e.stats.Snapshot().TotalReq != 0 || recorder.Code != 429 {
		t.Fatalf("forced call=%d failures=%d status=%d", calls.Load(), e.failureCount[1], recorder.Code)
	}
}

func TestCanceledConnectionWaitDoesNotRecordUpstreamAttempt(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer up.Close()
	e, p := newStreamEngine(t, up.URL, config.GlobalConfig{CBThreshold: 1})
	p.MaxConcurrent = 1
	e.rebuildConnLimiters(e.cfg)
	limiter, err := e.acquireConnSlot(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer e.releaseConnSlot(limiter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	time.AfterFunc(30*time.Millisecond, cancel)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(middleware.CtxFormatKey, "openai")
	c.Request = httptest.NewRequest("POST", "/chat/completions", strings.NewReader(`{"model":"m","stream":true}`)).WithContext(ctx)
	e.HandleRequest(c)
	if calls.Load() != 0 || e.failureCount[1] != 0 || e.stats.Snapshot().TotalReq != 0 {
		t.Fatalf("queue cancellation counted as upstream attempt: %+v", e.stats.Snapshot())
	}
}
