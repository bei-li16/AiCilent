package router

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

func TestHandleResponsesRequestUsesOpenAIChatProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["messages"] == nil || body["input"] != nil {
			t.Fatalf("unexpected upstream Responses conversion: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","model":"gpt-5-codex","choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name:     "chat-provider",
			ModelID:  "gpt-5-codex",
			APIKey:   "key",
			BaseURL:  upstream.URL + "/v1",
			Priority: 1,
			Format:   "openai",
			Timeout:  5,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	r := gin.New()
	r.Use(middleware.DetectFormat())
	r.POST("/v1/responses", e.HandleRequest)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","instructions":"Be concise","input":"ping"}`))
	r.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["object"] != "response" || response["status"] != "completed" || response["output_text"] != "pong" {
		t.Fatalf("unexpected Responses response: %#v", response)
	}
}
