package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"
	"ai-proxy/internal/tracer"

	"github.com/gin-gonic/gin"
)

func TestForwardRequestStreamDoesNotCommitEmptyUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	provider := config.Provider{
		Name:          "stream",
		ModelID:       "m",
		BaseURL:       upstream.URL + "/v1",
		Priority:      1,
		Format:        "openai",
		MaxConcurrent: 1,
	}
	cfg := &config.Config{
		Global:    config.GlobalConfig{MaxStreamMinutes: 1},
		Providers: []config.Provider{provider},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", &provider, 5, tr)
	if err == nil {
		t.Fatal("empty upstream stream returned nil error")
	}
	if ctx.Writer.Written() {
		t.Fatal("downstream response was committed before receiving an SSE event")
	}
}

func TestForwardRequestStreamCommitsFirstEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"ok\"}\n\n")
	}))
	defer upstream.Close()

	provider := config.Provider{Name: "stream", ModelID: "m", BaseURL: upstream.URL + "/v1", Priority: 1, Format: "openai"}
	cfg := &config.Config{Global: config.GlobalConfig{MaxStreamMinutes: 1}, Providers: []config.Provider{provider}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

	if err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", &provider, 5, tr); err != nil {
		t.Fatal(err)
	}
	if !ctx.Writer.Written() || recorder.Body.String() != "data: {\"id\":\"ok\"}\n\n" {
		t.Fatalf("unexpected downstream response: written=%v body=%q", ctx.Writer.Written(), recorder.Body.String())
	}
}

func TestForwardRequestStreamDoesNotCommitHeartbeatOrPartialEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := map[string]string{
		"heartbeat":     ": keep-alive\n\n",
		"partial event": "data: {\"id\":\"partial\"}",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, body)
			}))
			defer upstream.Close()

			provider := config.Provider{Name: "stream", ModelID: "m", BaseURL: upstream.URL + "/v1", Priority: 1, Format: "openai"}
			cfg := &config.Config{Global: config.GlobalConfig{MaxStreamMinutes: 1}, Providers: []config.Provider{provider}}
			e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
			tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)

			err := e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", &provider, 5, tr)
			if err == nil {
				t.Fatal("incomplete upstream stream returned nil error")
			}
			if ctx.Writer.Written() || recorder.Body.Len() != 0 {
				t.Fatalf("downstream committed incomplete stream: written=%v body=%q", ctx.Writer.Written(), recorder.Body.String())
			}
		})
	}
}

func TestForwardRequestStreamCommitsSplitEventOnlyAfterTerminator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	allowTerminator := make(chan struct{})
	wrotePartial := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"ok\"}")
		flusher.Flush()
		close(wrotePartial)
		<-allowTerminator
		_, _ = io.WriteString(w, "\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	provider := config.Provider{Name: "stream", ModelID: "m", BaseURL: upstream.URL + "/v1", Priority: 1, Format: "openai"}
	cfg := &config.Config{Global: config.GlobalConfig{MaxStreamMinutes: 1}, Providers: []config.Provider{provider}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
	tr := tracer.New("m", "openai", "req", io.Discard, io.Discard)
	errCh := make(chan error, 1)
	go func() {
		errCh <- e.forwardRequestStream(ctx, []byte(`{"model":"m","stream":true}`), "openai", &provider, 5, tr)
	}()

	<-wrotePartial
	time.Sleep(25 * time.Millisecond)
	if ctx.Writer.Written() {
		t.Fatal("downstream committed before the SSE event terminator")
	}
	close(allowTerminator)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if !ctx.Writer.Written() || recorder.Body.String() != "data: {\"id\":\"ok\"}\n\n" {
		t.Fatalf("unexpected downstream response: written=%v body=%q", ctx.Writer.Written(), recorder.Body.String())
	}
}

func TestHandleRequestFailsOverBeforeFirstStreamEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keep-alive\n\ndata: {\"id\":\"partial\"}")
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"fallback\"}\n\n")
	}))
	defer second.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1, MaxStreamMinutes: 1},
		Providers: []config.Provider{
			{Name: "first", ModelID: "m", BaseURL: first.URL + "/v1", Priority: 1, Format: "openai", Timeout: 5},
			{Name: "second", ModelID: "m", BaseURL: second.URL + "/v1", Priority: 1, Format: "openai", Timeout: 5},
		},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"m","stream":true}`))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != "data: {\"id\":\"fallback\"}\n\n" {
		t.Fatalf("unexpected failover body: %q", got)
	}
}
