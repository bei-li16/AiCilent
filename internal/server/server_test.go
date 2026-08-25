package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/version"
)

func TestVersionEndpoint(t *testing.T) {
	oldVersion, oldCommit, oldDate := version.Version, version.Commit, version.BuildDate
	version.Version, version.Commit, version.BuildDate = "v-test", "abc123", "2026-08-07T00:00:00Z"
	defer func() { version.Version, version.Commit, version.BuildDate = oldVersion, oldCommit, oldDate }()

	cfg := &config.Config{
		Global: config.GlobalConfig{ListenAddr: ":0"},
		Providers: []config.Provider{{
			Name: "test", ModelID: "test", APIKey: "key", BaseURL: "http://127.0.0.1", Format: "openai", Priority: 1,
		}},
	}
	srv := New(cfg, "")
	req := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["version"] != "v-test" || got["commit"] != "abc123" || got["build_date"] != "2026-08-07T00:00:00Z" {
		t.Fatalf("unexpected version response: %#v", got)
	}
	if gotID := res.Header().Get("X-Request-ID"); gotID == "" {
		t.Fatal("version response has no X-Request-ID")
	}
	srv.SaveStats()
	if srv.Rot != nil {
		_ = srv.Rot.Close()
	}
}

func TestResponsesAliasRoute(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-alias","model":"gpt-5-codex","choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{ListenAddr: ":0", CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name: "test", ModelID: "gpt-5-codex", APIKey: "key",
			BaseURL: upstream.URL + "/v1", Format: "openai", Priority: 1, Timeout: 5,
		}},
	}
	srv := New(cfg, "")
	defer func() {
		srv.SaveStats()
		if srv.Rot != nil {
			_ = srv.Rot.Close()
		}
	}()

	req := httptest.NewRequest(http.MethodPost, "/responses", strings.NewReader(`{"model":"gpt-5-codex","input":"ping"}`))
	res := httptest.NewRecorder()
	srv.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["object"] != "response" || body["output_text"] != "pong" {
		t.Fatalf("unexpected Responses response: %#v", body)
	}
}
