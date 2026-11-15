package router

import (
	"context"
	"errors"
	"fmt"
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

func schedulingConfig(baseURL string, names ...string) *config.Config {
	cfg := &config.Config{Global: config.GlobalConfig{CBThreshold: 10, MaxStreamMinutes: 1}}
	for _, name := range names {
		cfg.Providers = append(cfg.Providers, config.Provider{
			Name: name, APIKey: name, ModelID: "m", BaseURL: baseURL + "/" + name,
			Format: "openai", Priority: 1, Timeout: 5,
		})
	}
	return cfg
}

func schedulingCandidates(providers []config.Provider) []accountCandidate {
	candidates := make([]accountCandidate, len(providers))
	for i := range providers {
		candidates[i] = accountCandidate{provider: &providers[i], key: identifyAccount(&providers[i])}
	}
	return candidates
}

func schedulingRouter(e *Engine) *gin.Engine {
	r := gin.New()
	r.Use(middleware.Logger(io.Discard), middleware.DetectFormat())
	r.POST("/chat/completions", e.HandleRequest)
	r.POST("/v1/messages", e.HandleRequest)
	return r
}

func scheduleRequest(r http.Handler, streaming bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	body := fmt.Sprintf(`{"model":"P1","stream":%t,"session_id":"same-chat","messages":[{"role":"user","content":"hello"}]}`, streaming)
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(body)))
	return w
}

func writeSchedulingResponse(w http.ResponseWriter, streaming bool, account string) {
	if streaming {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"id\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n", account)
		return
	}
	fmt.Fprintf(w, `{"id":%q}`, account)
}

func assertIdleAccounts(t *testing.T, scheduler *accountScheduler) {
	t.Helper()
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	for _, load := range scheduler.loads {
		if load.active != 0 {
			t.Fatalf("account retained %d active attempts", load.active)
		}
	}
}

func TestAccountIdentityNormalizesOriginAndSeparatesPools(t *testing.T) {
	a := config.Provider{Name: "a", APIKey: "secret", BaseURL: "https://TOKEN.SENSENOVA.CN/v1", Format: "openai"}
	b := a
	b.Name, b.ModelID, b.Priority = "alias", "another-model", 3
	b.BaseURL = "https://token.sensenova.cn:443/api"
	if identifyAccount(&a) != identifyAccount(&b) {
		t.Fatal("same credential in one protocol pool did not share state")
	}
	b.Format = "anthropic"
	if identifyAccount(&a) == identifyAccount(&b) {
		t.Fatal("OpenAI and Anthropic pools share state")
	}
	b = a
	b.APIKey = "other-secret"
	if identifyAccount(&a) == identifyAccount(&b) {
		t.Fatal("different credentials share state")
	}
	b = a
	b.BaseURL = "http://token.sensenova.cn:8080"
	if identifyAccount(&a) == identifyAccount(&b) {
		t.Fatal("different upstream origins share state")
	}
}

func TestAccountSchedulerPrefersLeastActiveThenOldestAttempt(t *testing.T) {
	cfg := schedulingConfig("https://example.com", "a", "b", "c")
	s := newAccountScheduler(cfg)
	candidates := schedulingCandidates(cfg.Providers)
	now := time.Now()
	s.loads[candidates[0].key] = &accountLoad{active: 2, lastAttempt: now.Add(-time.Hour)}
	s.loads[candidates[1].key] = &accountLoad{lastAttempt: now.Add(-time.Second)}
	s.loads[candidates[2].key] = &accountLoad{lastAttempt: now.Add(-time.Minute)}
	eligible := func(string) error { return nil }
	i, lease, _ := s.selectAndStart(context.Background(), candidates, eligible)
	if i != 2 || lease.active != 1 || !lease.previousAttempt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("selected candidate=%d active=%d, want oldest idle account c", i, lease.active)
	}
	lease.finish()
	lease.finish()
	i, lease, _ = s.selectAndStart(context.Background(), candidates, eligible)
	if i != 1 {
		t.Fatalf("selected candidate=%d, want b after c was just attempted", i)
	}
	lease.finish()
}

func TestAccountSchedulerReservationsAreAtomicAndUncapped(t *testing.T) {
	cfg := schedulingConfig("https://example.com", "a", "b", "c")
	s := newAccountScheduler(cfg)
	const arrivals = 30
	leases := make(chan *accountLease, arrivals)
	var wg sync.WaitGroup
	for range arrivals {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, lease, _ := s.selectAndStart(context.Background(), schedulingCandidates(cfg.Providers), func(string) error { return nil })
			leases <- lease
		}()
	}
	wg.Wait()
	for _, provider := range cfg.Providers {
		if got := s.loads[identifyAccount(&provider)].active; got != arrivals/len(cfg.Providers) {
			t.Fatalf("active=%d, want balanced concurrency %d", got, arrivals/len(cfg.Providers))
		}
	}
	close(leases)
	for lease := range leases {
		lease.finish()
	}
	assertIdleAccounts(t, s)
}

func TestAccountSchedulerSharesModelsButKeepsProtocolPoolsIndependent(t *testing.T) {
	cfg := schedulingConfig("https://example.com", "a", "b")
	a := cfg.Providers[0]
	alias := a
	alias.Name, alias.ModelID, alias.Priority = "a-another-model", "other", 2
	anthropic := a
	anthropic.Name, anthropic.Format, anthropic.BaseURL = "a-anthropic", "anthropic", "https://example.com"
	anthropicB := cfg.Providers[1]
	anthropicB.Format = "anthropic"
	cfg.Providers = append(cfg.Providers, alias, anthropic, anthropicB)
	s := newAccountScheduler(cfg)
	busy := s.start(identifyAccount(&a))
	defer busy.finish()
	i, lease, _ := s.selectAndStart(context.Background(), schedulingCandidates([]config.Provider{alias, cfg.Providers[1]}), func(string) error { return nil })
	if i != 1 {
		t.Fatal("another model did not see the same account's existing load")
	}
	lease.finish()
	i, lease, _ = s.selectAndStart(context.Background(), schedulingCandidates([]config.Provider{anthropic, anthropicB}), func(string) error { return nil })
	if i != 0 || !lease.previousAttempt.IsZero() {
		t.Fatal("OpenAI load or recency leaked into the Anthropic pool")
	}
	lease.finish()
}

func TestAccountSchedulerSkipsIneligibleWithoutRecordingAttempt(t *testing.T) {
	cfg := schedulingConfig("https://example.com", "a", "b", "c")
	s := newAccountScheduler(cfg)
	var checked []string
	i, lease, skipped := s.selectAndStart(context.Background(), schedulingCandidates(cfg.Providers), func(name string) error {
		checked = append(checked, name)
		if name == "a" {
			return errors.New("cooling down")
		}
		return nil
	})
	if i != 1 || len(skipped) != 1 || strings.Join(checked, ",") != "a,b" {
		t.Fatalf("selected=%d checked=%v skipped=%v", i, checked, skipped)
	}
	a := s.loads[identifyAccount(&cfg.Providers[0])]
	if a.active != 0 || !a.lastAttempt.IsZero() {
		t.Fatal("skipping an ineligible account recorded a real attempt")
	}
	lease.finish()
}

func TestAccountSchedulerReloadPreservesActiveStateAndPrunesRemovedAccounts(t *testing.T) {
	cfg := schedulingConfig("https://example.com", "a", "b")
	s := newAccountScheduler(cfg)
	key := identifyAccount(&cfg.Providers[0])
	lease := s.start(key)
	before := s.loads[key].lastAttempt
	renamed := cfg.Providers[0]
	renamed.Name = "renamed-a"
	s.reconcile(&config.Config{Providers: []config.Provider{renamed}})
	if s.loads[key].active != 1 || !s.loads[key].lastAttempt.Equal(before) {
		t.Fatal("reload reset an active account or its recency")
	}
	s.reconcile(&config.Config{})
	if s.loads[key].active != 1 {
		t.Fatal("removed account was discarded while a request was active")
	}
	lease.finish()
	if len(s.loads) != 0 {
		t.Fatal("removed idle account was not pruned")
	}
}

func TestHandleRequestBalancesOverlappingAccountAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			started := make(chan string, 3)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				started <- account
				if streaming {
					w.Header().Set("Content-Type", "text/event-stream")
					io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
					w.(http.Flusher).Flush()
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				writeSchedulingResponse(w, streaming, account)
			}))
			t.Cleanup(up.Close)
			t.Cleanup(unblock)
			e := NewEngine(schedulingConfig(up.URL, "a", "b"), "", io.Discard, io.Discard, stats.New(""))
			r := schedulingRouter(e)
			done := make(chan *httptest.ResponseRecorder, 3)
			for _, want := range []string{"a", "b", "a"} {
				go func() { done <- scheduleRequest(r, streaming) }()
				select {
				case got := <-started:
					if got != want {
						t.Fatalf("selected=%s want=%s", got, want)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("new arrival was queued instead of dispatched")
				}
			}
			unblock()
			for range 3 {
				select {
				case response := <-done:
					if response.Code != http.StatusOK {
						t.Errorf("response=%d %s", response.Code, response.Body.String())
					}
				case <-time.After(6 * time.Second):
					t.Fatal("request did not finish")
				}
			}
			assertIdleAccounts(t, e.accounts)
		})
	}
}

func TestSuccessfulAccountIsNotStickyForNextRequestInSameSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeSchedulingResponse(w, false, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}))
	defer up.Close()
	e := NewEngine(schedulingConfig(up.URL, "a", "b"), "", io.Discard, io.Discard, stats.New(""))
	r := schedulingRouter(e)
	first := scheduleRequest(r, false)
	second := scheduleRequest(r, false)
	if first.Body.String() != `{"id":"a"}` || second.Body.String() != `{"id":"b"}` {
		t.Fatalf("same-session requests stuck to a successful account: %s then %s", first.Body, second.Body)
	}
	if first.Header().Get("X-Request-ID") == "" || first.Header().Get("X-Request-ID") == second.Header().Get("X-Request-ID") {
		t.Fatal("two HTTP requests did not get independent request IDs")
	}
	assertIdleAccounts(t, e.accounts)
}

func TestRateLimitFailoverReevaluatesCurrentAccountLoads(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			arrivals := make(chan string, 10)
			firstRelease := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(firstRelease) }) }
			var primaryOnce sync.Once
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				account := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				arrivals <- account
				if account == "a" {
					primaryOnce.Do(func() {
						select {
						case <-firstRelease:
						case <-r.Context().Done():
						}
					})
					w.WriteHeader(http.StatusTooManyRequests)
					io.WriteString(w, `{"error":"rate limited"}`)
					return
				}
				writeSchedulingResponse(w, streaming, account)
			}))
			t.Cleanup(up.Close)
			t.Cleanup(unblock)
			cfg := schedulingConfig(up.URL, "a", "alias-a", "b", "c")
			cfg.Providers[0].Retry.MaxRetries = 1
			cfg.Providers[1].APIKey = "a"
			e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
			r := schedulingRouter(e)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- scheduleRequest(r, streaming) }()
			select {
			case got := <-arrivals:
				if got != "a" {
					t.Fatalf("initial account=%s, want a", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("initial attempt did not start")
			}
			busyB := e.accounts.start(identifyAccount(&cfg.Providers[2]))
			t.Cleanup(busyB.finish)
			unblock()
			select {
			case response := <-done:
				if response.Code != 200 || !strings.Contains(response.Body.String(), `"c"`) {
					t.Fatalf("response=%d %s, want account c", response.Code, response.Body.String())
				}
			case <-time.After(6 * time.Second):
				t.Fatal("failover did not finish")
			}
			busyB.finish()
			if len(arrivals) != 2 || <-arrivals != "a" || <-arrivals != "c" {
				t.Fatal("retry budget, account alias exclusion, or dynamic failover order changed")
			}
			if e.failureCount[1] != 0 {
				t.Fatal("429 failure penalized the circuit breaker")
			}
			assertIdleAccounts(t, e.accounts)
		})
	}
}

func TestAccountLoadReleasedOnClientCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	started := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"started\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer up.Close()
	e := NewEngine(schedulingConfig(up.URL, "a"), "", io.Discard, io.Discard, stats.New(""))
	r := schedulingRouter(e)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		request := httptest.NewRequest(http.MethodPost, "/chat/completions", strings.NewReader(`{"model":"P1","stream":true}`)).WithContext(ctx)
		r.ServeHTTP(httptest.NewRecorder(), request)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled stream did not finish")
	}
	assertIdleAccounts(t, e.accounts)
	if snapshot := e.stats.Snapshot(); snapshot.TotalCanceled != 1 || snapshot.TotalFail != 0 {
		t.Fatalf("client cancellation misclassified: %+v", snapshot)
	}
}

func TestRetryBackoffDoesNotRetainAccountConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":"rate limited"}`)
			return
		}
		writeSchedulingResponse(w, false, "a")
	}))
	defer up.Close()
	cfg := schedulingConfig(up.URL, "a")
	cfg.Providers[0].Retry = config.RetryConfig{MaxRetries: 1, RetryInterval: 1, BackoffFactor: 1}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	r := schedulingRouter(e)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- scheduleRequest(r, false) }()
	deadline := time.Now().Add(500 * time.Millisecond)
	for e.stats.Snapshot().TotalFail != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first 429 was not recorded before the retry interval")
		}
		time.Sleep(time.Millisecond)
	}
	assertIdleAccounts(t, e.accounts)
	if second := scheduleRequest(r, false); second.Code != 200 {
		t.Fatalf("new request could not use account during backoff: %d", second.Code)
	}
	select {
	case first := <-done:
		if first.Code != 200 {
			t.Fatalf("retried request failed: %d", first.Code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("retry did not complete")
	}
	if calls.Load() != 3 {
		t.Fatalf("upstream calls=%d, want initial failure, new request, retry", calls.Load())
	}
	assertIdleAccounts(t, e.accounts)
}

func TestAccountLoadsReleasedAfterUpstreamFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, status := range []int{429, 500} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("status=%d/stream=%t", status, streaming), func(t *testing.T) {
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(status)
					io.WriteString(w, `{"error":"upstream failed"}`)
				}))
				defer up.Close()
				e := NewEngine(schedulingConfig(up.URL, "a"), "", io.Discard, io.Discard, stats.New(""))
				response := scheduleRequest(schedulingRouter(e), streaming)
				if response.Code < 400 || e.stats.Snapshot().TotalFail != 1 {
					t.Fatalf("failure was not recorded: status=%d", response.Code)
				}
				assertIdleAccounts(t, e.accounts)
			})
		}
	}
}
