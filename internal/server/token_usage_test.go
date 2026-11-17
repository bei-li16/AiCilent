package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/stats"
)

func TestUsageCapturesNativeAndConvertedResponsesAndPersists(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/v1/messages" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":10,\"cache_read_input_tokens\":20,\"cache_creation_input_tokens\":5,\"output_tokens\":0}}}\n\n"+
				"data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n"+
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":4}}\n\n"+
				"data: {\"type\":\"message_stop\"}\n\n")
			return
		}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(40 * time.Millisecond)
		fmt.Fprint(w, "data: {\"id\":\"chat-1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"!\"},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: {\"id\":\"chat-1\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":5}}}\n\n"+
			"data: [DONE]\n\n")
	}))
	defer upstream.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "providers.yaml")
	srv := New(&config.Config{
		Global: config.GlobalConfig{LogFile: filepath.Join(dir, "proxy.log")},
		Providers: []config.Provider{
			{Name: "chat", ModelID: "chat-model", BaseURL: upstream.URL + "/v1", Priority: 1, Format: "openai", Timeout: 5},
			{Name: "messages", ModelID: "messages-model", BaseURL: upstream.URL, Priority: 2, Format: "anthropic", Timeout: 5},
		},
	}, configPath)
	t.Cleanup(func() { srv.StopWatcher(); srv.SaveStats(); _ = srv.Rot.Close() })
	for _, tc := range []struct{ path, body, end string }{
		{"/v1/chat/completions", `{"model":"P1"}`, "prompt_tokens"},
		{"/v1/chat/completions", `{"model":"P1","stream":true}`, "[DONE]"},
		{"/v1/responses", `{"model":"P1","stream":true,"input":"hello"}`, "response.completed"},
		{"/v1/messages", `{"model":"P2","stream":true}`, "message_stop"},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), tc.end) {
			t.Fatalf("%s returned %d: %s", tc.path, rec.Code, rec.Body)
		}
	}
	snapshot := func() stats.UsageSnapshot {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))
		var usage stats.UsageSnapshot
		if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil || rec.Code != 200 {
			t.Fatalf("usage HTTP %d: %v", rec.Code, err)
		}
		return usage
	}
	usage := snapshot()
	if usage.Total.Requests != 4 || usage.Total.Input != 71 || usage.Total.Output != 13 ||
		usage.Total.CachedInput != 30 || usage.Total.CacheEligible != 71 ||
		usage.Total.FirstCount != 3 || usage.Total.DecodeTokens != 6 {
		t.Fatalf("incorrect aggregate: %+v", usage.Total)
	}
	if usage.Days[usage.Today]["chat-model"].Requests != 3 || usage.Days[usage.Today]["messages-model"].Input != 35 ||
		len(usage.Hours) == 0 || usage.StartedAt == nil {
		t.Fatalf("incorrect buckets: %+v", usage)
	}
	srv.SaveStats()
	restored := stats.New(statsFilePath(configPath)).TokenUsageSnapshot(time.Now())
	if restored.Total != usage.Total || restored.Days[usage.Today]["chat-model"].Requests != 3 {
		t.Fatalf("usage lost during restart: %+v", restored)
	}
	reset := func(path, addr string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := reset("/api/stats/reset", "127.0.0.1:5678"); code != http.StatusOK {
		t.Fatalf("request stats reset: HTTP %d", code)
	}
	if after := snapshot(); after.Total != usage.Total || len(after.Days) != len(usage.Days) {
		t.Fatalf("request stats reset changed token usage: %+v", after)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"P1"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("record post-reset request: HTTP %d", rec.Code)
	}
	if code := reset("/api/usage/reset", "192.0.2.1:5678"); code != http.StatusForbidden || snapshot().Total.Requests != 5 {
		t.Fatalf("unexpected response to remote reset: HTTP %d", code)
	}
	if code := reset("/api/usage/reset", "127.0.0.1:5678"); code != http.StatusOK {
		t.Fatalf("token usage reset: HTTP %d", code)
	}
	assertCleared := func(after stats.UsageSnapshot) {
		t.Helper()
		if after.Total != (stats.UsageTotals{}) || len(after.Days) != 0 || len(after.Hours) != 0 || after.StartedAt != nil {
			t.Fatalf("token reset did not clear all usage: %+v", after)
		}
	}
	assertCleared(snapshot())
	reloaded := stats.New(statsFilePath(configPath))
	assertCleared(reloaded.TokenUsageSnapshot(time.Now()))
	if requests := reloaded.Snapshot(); requests.TotalClientReq != 1 || requests.TotalReq != 1 || requests.TotalClientSuccess != 1 {
		t.Fatalf("token reset changed request statistics: %+v", requests)
	}
}
