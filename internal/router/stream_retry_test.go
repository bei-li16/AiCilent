package router

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-proxy/internal/config"
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
