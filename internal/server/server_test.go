package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
