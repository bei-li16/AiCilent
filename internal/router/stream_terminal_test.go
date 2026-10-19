package router

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/tracer"

	"github.com/gin-gonic/gin"
)

func TestTerminalWriterClassifiesStreams(t *testing.T) {
	tests := []struct {
		name           string
		writes         []string
		wantAnswered   bool
		wantTerminated bool
	}{
		{
			name: "openai null finish_reason then stop",
			writes: []string{
				"data: {\"choices\":[{\"delta\":{\"content\":\"a\"},\"finish_reason\":null}]}\n\n",
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
			},
			wantAnswered: true,
		},
		{
			name:           "openai done",
			writes:         []string{"data: {\"choices\":[]}\n\ndata: [DONE]\n\n"},
			wantTerminated: true,
		},
		{
			name:           "done split across writes",
			writes:         []string{"data: [DO", "NE]\n\n"},
			wantTerminated: true,
		},
		{
			name:           "done with crlf",
			writes:         []string{"data: [DONE]\r\n\r\n"},
			wantTerminated: true,
		},
		{
			name:           "done without space",
			writes:         []string{"data:[DONE]\n\n"},
			wantTerminated: true,
		},
		{
			name:           "anthropic message_stop event",
			writes:         []string{"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
			wantTerminated: true,
		},
		{
			name:         "anthropic stop_reason",
			writes:       []string{"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"},
			wantAnswered: true,
		},
		{
			name:           "responses completed",
			writes:         []string{"event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n"},
			wantTerminated: true,
		},
		{
			name:   "escaped content is not a marker",
			writes: []string{"data: {\"choices\":[{\"delta\":{\"content\":\"\\\"finish_reason\\\":\\\"stop\\\"\"}}]}\n\n"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf strings.Builder
			w := newTerminalWriter(&buf)
			var lastErr error
			for _, chunk := range tt.writes {
				_, lastErr = w.Write([]byte(chunk))
			}
			// A terminator implies the answer was complete, so Answered() is
			// expected for terminated streams as well.
			wantAnswered := tt.wantAnswered || tt.wantTerminated
			if w.Answered() != wantAnswered {
				t.Fatalf("Answered() = %v, want %v", w.Answered(), wantAnswered)
			}
			if w.Terminated() != tt.wantTerminated {
				t.Fatalf("Terminated() = %v, want %v", w.Terminated(), tt.wantTerminated)
			}
			if tt.wantTerminated && !errors.Is(lastErr, errStreamTerminal) {
				t.Fatalf("write error = %v, want errStreamTerminal", lastErr)
			}
			if !tt.wantTerminated && errors.Is(lastErr, errStreamTerminal) {
				t.Fatal("unexpected errStreamTerminal")
			}
			if buf.Len() == 0 {
				t.Fatal("bytes were not forwarded downstream")
			}
		})
	}
}

func newStreamEngine(t *testing.T, upstreamURL string, global config.GlobalConfig) (*Engine, *config.Provider) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if global.MaxStreamMinutes == 0 {
		global.MaxStreamMinutes = 1
	}
	cfg := &config.Config{
		Global: global,
		Providers: []config.Provider{{
			Name: "p", ModelID: "m", BaseURL: upstreamURL + "/v1",
			Priority: 1, Format: "openai", Timeout: 5,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	return e, &cfg.Providers[0]
}

// A terminator must end the proxy's upstream read immediately: the client will
// close as soon as it sees it, and continuing to read manufactures a spurious
// "context canceled" against a healthy backend.
func TestForwardRequestStreamStopsAfterTerminator(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer upstream.Close()
	defer close(release)

	e, provider := newStreamEngine(t, upstream.URL, config.GlobalConfig{})
	provider.Timeout = 30

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	start := time.Now()
	err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", provider, 30, tr)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("forwardRequestStream error = %v, want nil", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("stream stop took %v; terminator did not short-circuit the read", elapsed)
	}
	if !strings.Contains(recorder.Body.String(), "[DONE]") {
		t.Fatalf("terminator was not forwarded: %q", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "stream interrupted") {
		t.Fatalf("completed stream was annotated as interrupted: %q", recorder.Body.String())
	}
}

func TestForwardRequestStreamClientGoneAfterSemanticFinishIsCanceled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	e, provider := newStreamEngine(t, upstream.URL, config.GlobalConfig{})
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil).WithContext(cctx)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	time.AfterFunc(100*time.Millisecond, cancel)
	err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", provider, 10, tr)

	if !errors.Is(err, errClientGoneIncomplete) {
		t.Fatalf("error = %v, want errClientGoneIncomplete", err)
	}
}

func TestForwardRequestStreamClientGoneMidStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	e, provider := newStreamEngine(t, upstream.URL, config.GlobalConfig{})
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil).WithContext(cctx)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	time.AfterFunc(100*time.Millisecond, cancel)
	err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", provider, 10, tr)

	if !errors.Is(err, errClientGoneIncomplete) {
		t.Fatalf("error = %v, want errClientGoneIncomplete", err)
	}
}

// A client disconnect must never feed the circuit breaker, otherwise two
// finished requests trip the breaker and the next request gets a 503.
func TestHandleRequestClientCancelDoesNotTripCircuitBreaker(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Global: config.GlobalConfig{
			CBThreshold: 2, CBCooldown: 10, CBSkipRequests: 1, MaxStreamMinutes: 1,
		},
		Providers: []config.Provider{{
			Name: "p", ModelID: "m", BaseURL: upstream.URL + "/v1",
			Priority: 1, Format: "openai", Timeout: 10,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)

	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"m","stream":true}`)).WithContext(cctx)
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(recorder, request)
		close(done)
	}()

	time.AfterFunc(150*time.Millisecond, cancel)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish after client cancel")
	}

	e.cbMu.Lock()
	failures := e.failureCount[1]
	open := e.circuitOpenSince[1] != nil
	e.cbMu.Unlock()

	if failures != 0 || open {
		t.Fatalf("client cancel tripped the breaker: failures=%d open=%v", failures, open)
	}
	if code := recorder.Code; code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
}

func TestShouldSkipGroupHalfOpenProbe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Global: config.GlobalConfig{CBThreshold: 2, CBCooldown: 60, CBSkipRequests: 1}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	e.recordGroupFailure("m", 1, tr, 2, 60, 1)
	e.recordGroupFailure("m", 1, tr, 2, 60, 1)

	if !e.shouldSkipGroup(1, "m", tr, 2, 60, 1, false) {
		t.Fatal("expected the open breaker to skip a normal request")
	}
	if e.shouldSkipGroup(1, "m", tr, 2, 60, 1, true) {
		t.Fatal("half-open probe was not released")
	}
	if !e.shouldSkipGroup(1, "m", tr, 2, 60, 1, false) {
		t.Fatal("a second request entered while the probe was in flight")
	}
	e.closeCircuitBreaker(1, "m", tr, 2, 60, 1)
	if e.shouldSkipGroup(1, "m", tr, 2, 60, 1, false) {
		t.Fatal("successful probe did not close the breaker")
	}
}

func TestRateLimitCooldownSkipsProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Global: config.GlobalConfig{RateLimitCooldown: 30}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))

	if err := e.checkRateLimit("p"); err != nil {
		t.Fatalf("unexpected initial rate limit error: %v", err)
	}
	e.markProviderCooldown("p")
	err := e.checkRateLimit("p")
	if err == nil {
		t.Fatal("expected a cooled-down provider to be rejected")
	}
	var apiErr *adapter.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want 429 APIError", err)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Mirrors production: the client reads SSE until [DONE] and closes the body
// immediately, while the upstream keeps the connection open. The request must
// be recorded as SUCCESS and must not feed the circuit breaker.
func TestHandleRequestClientCloseAfterDoneIsSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 2, CBCooldown: 10, CBSkipRequests: 1, MaxStreamMinutes: 1},
		Providers: []config.Provider{{
			Name: "p", ModelID: "m", BaseURL: upstream.URL + "/v1",
			Priority: 1, Format: "openai", Timeout: 10,
		}},
	}
	logs := &syncBuffer{}
	e := NewEngine(cfg, "", logs, logs, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)
	proxy := httptest.NewServer(router)
	defer proxy.Close()

	resp, err := http.Post(proxy.URL+"/chat/completions", "application/json",
		strings.NewReader(`{"model":"m","stream":true}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	scanner := bufio.NewScanner(resp.Body)
	sawDone := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "[DONE]") {
			sawDone = true
			break
		}
	}
	_ = resp.Body.Close()
	if !sawDone {
		t.Fatalf("client never received [DONE]; log:/n%s", logs.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(logs.String(), "result=SUCCESS") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got := logs.String(); !strings.Contains(got, "result=SUCCESS") {
		t.Fatalf("request not recorded as success:/n%s", got)
	}
	if strings.Contains(logs.String(), "context canceled") {
		t.Fatalf("healthy stream logged context canceled:/n%s", logs.String())
	}

	e.cbMu.Lock()
	failures, open := e.failureCount[1], e.circuitOpenSince[1] != nil
	e.cbMu.Unlock()
	if failures != 0 || open {
		t.Fatalf("breaker moved on a completed stream: failures=%d open=%v", failures, open)
	}
}
