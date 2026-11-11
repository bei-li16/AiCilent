package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

// TestRetryWaitPacedFromAttemptStart verifies that the wait between same-provider
// attempts is paced from the attempt's start, not stacked on top of its latency:
// an attempt that returns 429 after 500ms with retry_interval: 1 fires the next
// attempt at ~1.0s, not at 1.5s; an attempt slower than the interval fires the
// next one immediately on return.
func TestRetryWaitPacedFromAttemptStart(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name          string
		upstreamDelay time.Duration
		// wantGap bounds the time between attempt starts.
		minGap time.Duration
		maxGap time.Duration
	}{
		// Fast 429 (500ms) with interval 1s: old fixed-delay waited 1.5s,
		// paced waits only the remaining 500ms.
		{name: "fast response waits only the remainder", upstreamDelay: 500 * time.Millisecond, minGap: 950 * time.Millisecond, maxGap: 1450 * time.Millisecond},
		// Slow 429 (1.2s) already covers the 1s interval: fire immediately
		// on return instead of adding the full wait (old: >= 2.2s).
		{name: "slow response retries immediately", upstreamDelay: 1200 * time.Millisecond, minGap: 1150 * time.Millisecond, maxGap: 2000 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var primaryCalls atomic.Int32
			arrivals := make(chan time.Time, 8)
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				arrivals <- time.Now()
				if primaryCalls.Add(1) < 3 {
					time.Sleep(tc.upstreamDelay)
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, `{"error":{"message":"rate limited"}}`)
					return
				}
				fmt.Fprint(w, `{"id":"primary"}`)
			}))
			defer up.Close()

			cfg := loadRateLimitRetryConfig(t, up.URL,
				"      max_retries: 2\n      retry_interval: 1\n      backoff_factor: 1", 0, "  rate_limit_cooldown: 30\n")
			e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
			t.Cleanup(e.transport.CloseIdleConnections)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Set(middleware.CtxFormatKey, "openai")
			c.Request = httptest.NewRequest(http.MethodPost, "/chat/completions",
				strings.NewReader(`{"model":"m"}`))
			start := time.Now()
			e.HandleRequest(c)

			if got := int(primaryCalls.Load()); got != 3 {
				t.Fatalf("primary attempts = %d, want 3; body=%s", got, recorder.Body.String())
			}
			first, second := <-arrivals, <-arrivals
			gap := second.Sub(first)
			if gap < tc.minGap || gap >= tc.maxGap {
				t.Fatalf("gap between attempt starts = %v, want [%v, %v) (fixed-delay behavior would be >= %v)",
					gap, tc.minGap, tc.maxGap, tc.upstreamDelay+time.Second)
			}
			if total := time.Since(start); total > gap+2*tc.upstreamDelay+2*time.Second {
				t.Fatalf("request took %v, unexpectedly slow", total)
			}
		})
	}
}
