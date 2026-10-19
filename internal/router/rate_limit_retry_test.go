package router

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

func loadRateLimitRetryConfig(t *testing.T, baseURL, retryYAML string, fallbackPriority int, cooldownYAML string) *config.Config {
	t.Helper()
	source := fmt.Sprintf(`global:
  cb_threshold: 1
%sproviders:
  - name: primary
    model_id: m
    api_key: test
    base_url: %s/primary
    priority: 1
    format: openai
    retry:
%s
`, cooldownYAML, baseURL, retryYAML)
	if fallbackPriority > 0 {
		source += fmt.Sprintf(`  - name: fallback
    model_id: m
    api_key: test
    base_url: %s/fallback
    priority: %d
    format: openai
    retry:
      max_retries: 1
      retry_interval: 1
      backoff_factor: 1
`, baseURL, fallbackPriority)
	}
	path := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestHandleRequestRateLimitRetriesFromYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name             string
		retryYAML        string
		recover          bool
		fallbackPriority int
	}{
		{
			name:             "constant interval recovers before failover",
			retryYAML:        "      max_retries: 2\n      retry_interval: 1\n      backoff_factor: 1",
			recover:          true,
			fallbackPriority: 1,
		},
		{
			name:             "exponential backoff recovers before failover",
			retryYAML:        "      max_retries: 2\n      retry_interval: 1\n      backoff_factor: 2",
			recover:          true,
			fallbackPriority: 1,
		},
		{
			name:             "configured interval and retry count",
			retryYAML:        "      max_retries: 1\n      retry_interval: 2\n      backoff_factor: 1",
			recover:          true,
			fallbackPriority: 1,
		},
		{
			name:             "exhausted retries switch account",
			retryYAML:        "      max_retries: 2\n      retry_interval: 1\n      backoff_factor: 1",
			fallbackPriority: 1,
		},
		{
			name:             "exhausted retries switch priority",
			retryYAML:        "      max_retries: 1\n      retry_interval: 1\n      backoff_factor: 1",
			fallbackPriority: 2,
		},
		{
			name:      "exhausted retries return 429 without fallback",
			retryYAML: "      max_retries: 1\n      retry_interval: 1\n      backoff_factor: 1",
		},
	}
	for _, tc := range cases {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				type attempt struct {
					path string
					at   time.Time
				}
				attempts := make(chan attempt, 10)
				var primaryCalls atomic.Int32
				var recoverOn atomic.Int32
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					attempts <- attempt{path: r.URL.Path, at: time.Now()}
					provider := "fallback"
					if strings.HasPrefix(r.URL.Path, "/primary/") {
						provider = "primary"
						if call := primaryCalls.Add(1); !tc.recover || call < recoverOn.Load() {
							w.WriteHeader(http.StatusTooManyRequests)
							io.WriteString(w, `{"error":{"message":"rate limited"}}`)
							return
						}
					}
					if streaming {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: {\"id\":\"%s\"}\n\ndata: [DONE]\n\n", provider)
					} else {
						fmt.Fprintf(w, `{"id":%q}`, provider)
					}
				}))
				defer up.Close()
				cfg := loadRateLimitRetryConfig(t, up.URL, tc.retryYAML, tc.fallbackPriority, "  rate_limit_cooldown: 30\n")
				policy := cfg.Providers[0].Retry
				recoverOn.Store(int32(policy.MaxRetries + 1))
				e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
				t.Cleanup(e.transport.CloseIdleConnections)
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Set(middleware.CtxFormatKey, "openai")
				c.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%t}`, streaming)))
				e.HandleRequest(c)

				wantCalls := policy.MaxRetries + 1
				if got := int(primaryCalls.Load()); got != wantCalls {
					t.Fatalf("primary attempts = %d, want YAML max_retries + 1 = %d; body=%s", got, wantCalls, recorder.Body.String())
				}
				var previous time.Time
				for i := 0; i < wantCalls; i++ {
					call := <-attempts
					if !strings.HasPrefix(call.path, "/primary/") {
						t.Fatalf("attempt %d switched provider before exhausting YAML retry budget: %s", i+1, call.path)
					}
					if i > 0 {
						wantWait := time.Duration(float64(policy.RetryInterval)*math.Pow(policy.BackoffFactor, float64(i-1))) * time.Second
						if got := call.at.Sub(previous); got < wantWait-50*time.Millisecond {
							t.Fatalf("retry %d waited %v, want at least YAML backoff %v", i, got, wantWait)
						}
					}
					previous = call.at
				}
				wantProvider := "primary"
				wantStatus := http.StatusOK
				if !tc.recover {
					if tc.fallbackPriority > 0 {
						wantProvider = "fallback"
						if len(attempts) != 1 || !strings.HasPrefix((<-attempts).path, "/fallback/") {
							t.Fatal("exhausted retries did not call the fallback exactly once")
						}
					} else {
						wantStatus = http.StatusTooManyRequests
					}
				}
				if len(attempts) != 0 {
					t.Fatalf("unexpected extra upstream attempts: %d", len(attempts))
				}
				if recorder.Code != wantStatus || (wantStatus == http.StatusOK && !strings.Contains(recorder.Body.String(), wantProvider)) {
					t.Fatalf("status=%d body=%s, want status=%d provider=%s", recorder.Code, recorder.Body.String(), wantStatus, wantProvider)
				}
				if err := e.checkRateLimit("primary"); (err == nil) != tc.recover {
					t.Fatalf("cooldown error = %v, want cooldown only after exhausting 429 retries", err)
				}
				if e.failureCount[1] != 0 {
					t.Fatal("429 retries penalized the circuit breaker")
				}
			})
		}
	}
}

func TestHandleRequestRateLimitRetryWaitHonorsCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var calls atomic.Int32
			firstCall := make(chan struct{})
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					close(firstCall)
				}
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer up.Close()
			cfg := loadRateLimitRetryConfig(t, up.URL, "      max_retries: 2\n      retry_interval: 10\n      backoff_factor: 1", 1, "  rate_limit_cooldown: 30\n")
			e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
			t.Cleanup(e.transport.CloseIdleConnections)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Set(middleware.CtxFormatKey, "openai")
			c.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%t}`, streaming))).WithContext(ctx)
			done := make(chan struct{})
			go func() {
				e.HandleRequest(c)
				close(done)
			}()
			select {
			case <-firstCall:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream was not called")
			}
			select {
			case <-done:
				t.Fatal("request ended before the configured retry wait")
			case <-time.After(50 * time.Millisecond):
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not interrupt the YAML retry wait")
			}
			if calls.Load() != 1 || e.failureCount[1] != 0 {
				t.Fatalf("canceled backoff retried, failed over, or penalized the breaker: calls=%d failures=%d", calls.Load(), e.failureCount[1])
			}
		})
	}
}

func TestSequentialRequestsAfterRateLimitFromYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, cooldownYAML := range []string{"", "  rate_limit_cooldown: 0\n", "  rate_limit_cooldown: 30\n"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("cooldown=%q/stream=%t", cooldownYAML, streaming), func(t *testing.T) {
				var calls atomic.Int32
				var recovered atomic.Bool
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if !recovered.Load() {
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if streaming {
						io.WriteString(w, "data: {\"id\":\"recovered\"}\n\ndata: [DONE]\n\n")
					} else {
						io.WriteString(w, `{"id":"recovered"}`)
					}
				}))
				defer up.Close()
				cfg := loadRateLimitRetryConfig(t, up.URL, "      max_retries: 1\n      retry_interval: 1\n      backoff_factor: 1", 1, cooldownYAML)
				e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
				t.Cleanup(e.transport.CloseIdleConnections)
				send := func() *httptest.ResponseRecorder {
					recorder := httptest.NewRecorder()
					c, _ := gin.CreateTestContext(recorder)
					c.Set(middleware.CtxFormatKey, "openai")
					c.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"P1","stream":%t}`, streaming)))
					e.HandleRequest(c)
					return recorder
				}
				var attemptsPerRequest int32
				for _, provider := range cfg.Providers {
					attemptsPerRequest += int32(provider.Retry.MaxRetries + 1)
				}
				for request := int32(1); request <= 2; request++ {
					response := send()
					if response.Code != http.StatusTooManyRequests {
						t.Fatalf("request %d status = %d, want 429", request, response.Code)
					}
					wantCalls := request * attemptsPerRequest
					if cfg.Global.RateLimitCooldown > 0 {
						wantCalls = attemptsPerRequest
					}
					if got := calls.Load(); got != wantCalls {
						t.Fatalf("after request %d: upstream calls = %d, want %d; cooldown must be explicitly enabled in YAML", request, got, wantCalls)
					}
				}
				if cfg.Global.RateLimitCooldown <= 0 {
					recovered.Store(true)
					response := send()
					if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "recovered") || calls.Load() != 2*attemptsPerRequest+1 {
						t.Fatalf("new request did not reach recovered upstream: status=%d calls=%d body=%s", response.Code, calls.Load(), response.Body.String())
					}
				}
			})
		}
	}
}

func TestDisablingRateLimitCooldownByReloadClearsExistingCooldowns(t *testing.T) {
	cfg := loadRateLimitRetryConfig(t, "http://127.0.0.1:1", "      max_retries: 1", 1, "  rate_limit_cooldown: 30\n")
	path := filepath.Join(t.TempDir(), "providers.yaml")
	e := NewEngine(cfg, path, io.Discard, io.Discard, stats.New(""))
	e.markProviderCooldown("primary")
	e.markProviderCooldown("fallback")
	updated := *cfg
	updated.Global.RateLimitCooldown = 0
	data, err := yaml.Marshal(&updated)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.reloadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"primary", "fallback"} {
		if err := e.checkRateLimit(name); err != nil {
			t.Fatalf("provider %s still skipped after disabling cooldown: %v", name, err)
		}
	}
	if len(e.coolUntil) != 0 {
		t.Fatal("disabled cooldown left stale deadlines in memory")
	}
}
