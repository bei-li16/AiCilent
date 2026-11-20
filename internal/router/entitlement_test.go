package router

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/adapter"
	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

// entitlementBody is byte-for-byte what sensenova returns for an account whose
// token plan is used up. It shares status 429 and code "8" with ordinary
// rps/rpm/tpm throttling, so the message is the only distinguishing signal.
const entitlementBody = `{"error":{"message":"token plan entitlement exhausted","type":"quota_exceeded_error","code":"8"}}`

// An exhausted plan must not consume the retry budget: retrying a key whose
// quota is gone cannot succeed, and each attempt costs seconds. The account is
// parked and the next one is tried immediately.
func TestEntitlementExhaustedParksAccountWithoutRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			var primaryCalls, fallbackCalls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, "/primary/") {
					primaryCalls.Add(1)
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, entitlementBody)
					return
				}
				fallbackCalls.Add(1)
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"id\":\"fallback\"}\n\ndata: [DONE]\n\n")
					return
				}
				io.WriteString(w, `{"id":"fallback"}`)
			}))
			defer up.Close()

			cfg := loadRateLimitRetryConfig(t, up.URL,
				"      max_retries: 2\n      retry_interval: 1\n      backoff_factor: 1", 1,
				"  rate_limit_cooldown: 30\n  entitlement_cooldown: 300\n")
			e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
			t.Cleanup(e.transport.CloseIdleConnections)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Set(middleware.CtxFormatKey, "openai")
			c.Request = httptest.NewRequest(http.MethodPost, "/chat/completions",
				strings.NewReader(fmt.Sprintf(`{"model":"m","stream":%t}`, streaming)))
			e.HandleRequest(c)

			if got := int(primaryCalls.Load()); got != 1 {
				t.Fatalf("primary attempts = %d, want 1: an exhausted entitlement must not be retried", got)
			}
			if got := int(fallbackCalls.Load()); got != 1 {
				t.Fatalf("fallback attempts = %d, want 1", got)
			}
			if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "fallback") {
				t.Fatalf("status=%d body=%s, want 200 served by fallback", recorder.Code, recorder.Body.String())
			}
			if e.failureCount[1] != 0 {
				t.Fatal("an exhausted entitlement penalized the circuit breaker")
			}

			err := e.checkRateLimit("primary")
			if err == nil {
				t.Fatal("expected the exhausted account to be parked")
			}
			if !strings.Contains(err.Error(), "entitlement exhausted") {
				t.Fatalf("park message = %v, want it to name the entitlement, not a generic cooldown", err)
			}
			var apiErr *adapter.APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("error = %v, want a 429 APIError", err)
			}
			if err := e.checkRateLimit("fallback"); err != nil {
				t.Fatalf("healthy account was parked: %v", err)
			}
			if got := len(e.quotaUntil); got != 1 {
				t.Fatalf("parked accounts = %d, want only the exhausted one", got)
			}
		})
	}
}

// The park has to outlast the ordinary rate-limit cooldown, otherwise the
// account comes back 30s later and burns another attempt on a dead plan.
func TestEntitlementParkOutranksRateLimitCooldown(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{Global: config.GlobalConfig{
		RateLimitCooldown:   30,
		EntitlementCooldown: 1800,
	}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	t.Cleanup(e.transport.CloseIdleConnections)

	e.markProviderCooldown("p")
	e.markProviderQuotaExhausted("p")

	e.rlMu.Lock()
	coolLeft := time.Until(e.coolUntil["p"])
	quotaLeft := time.Until(e.quotaUntil["p"])
	e.rlMu.Unlock()
	if quotaLeft <= coolLeft {
		t.Fatalf("entitlement park %v must exceed the rate-limit cooldown %v", quotaLeft, coolLeft)
	}

	err := e.checkRateLimit("p")
	if err == nil || !strings.Contains(err.Error(), "token plan entitlement exhausted") {
		t.Fatalf("error = %v, want the entitlement reason to win over the plain cooldown", err)
	}
}

func TestEntitlementCooldownConfig(t *testing.T) {
	cases := []struct {
		name    string
		config  int
		wantSec int
		park    bool
	}{
		// 0 and "key absent" are the same path: the shipped default is 5 minutes.
		{name: "unset defaults to 5m", config: 0, wantSec: 300, park: true},
		{name: "explicit value is honored", config: 120, wantSec: 120, park: true},
		{name: "negative disables the special handling", config: -1, wantSec: 0, park: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			e := NewEngine(&config.Config{Global: config.GlobalConfig{
				RateLimitCooldown:   30,
				EntitlementCooldown: tc.config,
			}}, "", io.Discard, io.Discard, stats.New(""))
			t.Cleanup(e.transport.CloseIdleConnections)

			if got := e.entitlementCooldownSec(); got != tc.wantSec {
				t.Fatalf("entitlementCooldownSec() = %d, want %d", got, tc.wantSec)
			}
			parked := e.markProviderQuotaExhausted("p")
			if tc.park && parked <= 0 {
				t.Fatalf("markProviderQuotaExhausted() = %v, want a positive park", parked)
			}
			if !tc.park && parked != 0 {
				t.Fatalf("markProviderQuotaExhausted() = %v, want 0 when disabled", parked)
			}
			if !tc.park {
				// Disabled: the caller falls back to the plain 429 cooldown.
				if err := e.checkRateLimit("p"); err != nil {
					t.Fatalf("disabled handling still parked the account: %v", err)
				}
			}
		})
	}
}
