package router

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

func TestUpstreamOriginIgnoresPathAndDefaultPort(t *testing.T) {
	tests := map[string]string{
		"https://TOKEN.SENSENOVA.CN/v1":      "https://token.sensenova.cn",
		"https://token.sensenova.cn:443/api": "https://token.sensenova.cn",
		"http://example.com:8080/v1":         "http://example.com:8080",
	}
	for input, want := range tests {
		if got := upstreamOrigin(input); got != want {
			t.Errorf("upstreamOrigin(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestForwardRequestEnforcesSharedHostLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	var active atomic.Int32
	var maximum atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer upstream.Close()

	provider := config.Provider{
		Name:          "limited",
		ModelID:       "m",
		BaseURL:       upstream.URL + "/v1",
		Format:        "openai",
		MaxConcurrent: 2,
	}
	cfg := &config.Config{Providers: []config.Provider{provider}}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/chat/completions", nil)
			errs <- e.forwardRequest(ctx, []byte(`{"model":"m"}`), "openai", &provider, 5, "")
		}()
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("two upstream requests did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("third request reached upstream before a slot was released")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum upstream concurrency = %d, want 2", got)
	}
}

func TestHandleRequestAllowsConfiguredProviderConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		_, _ = io.WriteString(w, `{"id":"ok"}`)
	}))
	defer upstream.Close()

	cfg := &config.Config{
		Global: config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		Providers: []config.Provider{{
			Name:          "concurrent",
			ModelID:       "m",
			BaseURL:       upstream.URL + "/v1",
			Priority:      1,
			Format:        "openai",
			Timeout:       5,
			MaxConcurrent: 2,
		}},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)

	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"m"}`))
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Errorf("status = %d, body=%s", recorder.Code, recorder.Body.String())
			}
		}()
	}

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("configured concurrent requests did not both reach the provider")
		}
	}
	close(release)
	wg.Wait()
}

func TestHostLimiterBlocksAndHonorsCancellation(t *testing.T) {
	limiter := newHostLimiter(1)
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := limiter.acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second acquire error = %v, want deadline exceeded", err)
	}

	limiter.release()
	if err := limiter.acquire(context.Background()); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	limiter.release()
}

func TestRebuildConnLimitersSharesStrictestOriginLimit(t *testing.T) {
	e := &Engine{}
	cfg := &config.Config{Providers: []config.Provider{
		{BaseURL: "https://token.sensenova.cn/v1", MaxConcurrent: 3},
		{BaseURL: "https://token.sensenova.cn", MaxConcurrent: 2},
	}}
	e.rebuildConnLimiters(cfg)

	limiter := e.connLimiters["https://token.sensenova.cn"]
	if limiter == nil {
		t.Fatal("shared origin limiter was not created")
	}
	limiter.mu.Lock()
	got := limiter.limit
	limiter.mu.Unlock()
	if got != 2 {
		t.Fatalf("limit = %d, want strictest value 2", got)
	}

	e.rebuildConnLimiters(&config.Config{Providers: []config.Provider{
		{BaseURL: "https://token.sensenova.cn/v1", MaxConcurrent: 1},
	}})
	limiter.mu.Lock()
	got = limiter.limit
	limiter.mu.Unlock()
	if got != 1 {
		t.Fatalf("hot-reloaded limit = %d, want 1", got)
	}
}
