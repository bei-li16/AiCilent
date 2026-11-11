package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/stats"
)

func TestRequestTimingIncludesRetryAndMeasuresFirstStreamWrite(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"rate limited"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
		w.(http.Flusher).Flush()
		time.Sleep(75 * time.Millisecond)
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()

	logPath := filepath.Join(t.TempDir(), "proxy.log")
	cfg := &config.Config{
		Global: config.GlobalConfig{LogFile: logPath, CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name: "mock", ModelID: "test", APIKey: "key", BaseURL: upstream.URL + "/v1", Format: "openai", Priority: 1, Timeout: 5,
			Retry: config.RetryConfig{MaxRetries: 1, RetryInterval: 1, BackoffFactor: 1},
		}},
	}
	srv := New(cfg, "")
	defer func() { srv.StopWatcher(); _ = srv.Rot.Close() }()
	post := func(model string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		srv.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(
			fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"test"}]}`, model))))
		return res
	}
	success := post("P1")
	if success.Code != 200 || attempts.Load() != 2 || !strings.Contains(success.Body.String(), "[DONE]") {
		t.Fatalf("expected two attempts and complete stream, got HTTP %d, %d attempts, %s", success.Code, attempts.Load(), success.Body)
	}
	failed := post("P9")
	if failed.Code != 503 || attempts.Load() != 2 {
		t.Fatalf("expected local 503 without upstream, got HTTP %d, %d attempts", failed.Code, attempts.Load())
	}
	snapshot := httptest.NewRecorder()
	srv.ServeHTTP(snapshot, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	var result stats.Snapshot
	if err := json.Unmarshal(snapshot.Body.Bytes(), &result); err != nil || len(result.RecentRequests) != 2 {
		t.Fatalf("invalid request timings: %+v, %v", result.RecentRequests, err)
	}
	sample, errorSample := result.RecentRequests[0], result.RecentRequests[1]
	if result.TotalClientReq != 2 || result.TotalClientCompleted != 2 || result.TotalClientSuccess != 1 || result.TotalReq != 2 {
		t.Fatalf("retry and local failure must not inflate successful client requests: requests=%d completed=%d successful=%d upstream attempts=%d",
			result.TotalClientReq, result.TotalClientCompleted, result.TotalClientSuccess, result.TotalReq)
	}
	if sample.RequestID != success.Header().Get("X-Request-ID") || sample.Model != "P1" ||
		sample.FirstResponseMs == nil || *sample.FirstResponseMs < 950 || sample.TotalMs-*sample.FirstResponseMs < 50 {
		t.Fatalf("success timing must include retry and separate first write from finish: %+v", sample)
	}
	if errorSample.RequestID != failed.Header().Get("X-Request-ID") || errorSample.StatusCode != 503 ||
		errorSample.FirstResponseMs != nil || errorSample.TotalMs < 0 {
		t.Fatalf("failed request must have total only: %+v", errorSample)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	first := strings.Index(string(log), "["+sample.RequestID+"] FIRST_RESPONSE")
	stream := strings.Index(string(log), "["+sample.RequestID+"] STREAM")
	summary := strings.Index(string(log), "["+sample.RequestID+"] SUMMARY")
	if first < 0 || stream <= first || summary <= stream || strings.Count(string(log), " FIRST_RESPONSE ") != 1 {
		t.Fatalf("expected one first-response event before stream completion: %s", log)
	}
	match := regexp.MustCompile(`request_elapsed=([^ ]+) \| end_reason=protocol_complete`).FindStringSubmatch(string(log[stream:summary]))
	if len(match) != 2 {
		t.Fatalf("stream completion is missing its request-relative elapsed time: %s", log[stream:summary])
	}
	elapsed, err := time.ParseDuration(match[1])
	elapsedMs := float64(elapsed) / float64(time.Millisecond)
	if err != nil || elapsedMs <= *sample.FirstResponseMs || elapsedMs > sample.TotalMs {
		t.Fatalf("stream elapsed=%s (%v), must be between first response and total duration: %+v", elapsed, err, sample)
	}
	if strings.Count(string(log), "retry_start_gap=") != 1 {
		t.Fatalf("only the second attempt should report a retry start gap: %s", log)
	}
}
