package router

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/middleware"
	"ai-proxy/internal/stats"

	"github.com/gin-gonic/gin"
)

func aliasEngine(routes []config.ModelRoute, providers ...*config.Provider) *Engine {
	cfg := &config.Config{
		Providers:   make([]config.Provider, len(providers)),
		ModelRoutes: routes,
	}
	for i, p := range providers {
		cfg.Providers[i] = *p
	}
	return &Engine{cfg: cfg, matcher: NewMatcher(routes)}
}

func assertOrderedNames(t *testing.T, e *Engine, modelName string, expected ...string) {
	t.Helper()
	got := e.getOrderedProviders(modelName)
	if len(got) != len(expected) {
		t.Fatalf("%s: got %d providers (%v), want %d (%v)",
			modelName, len(got), providerNames(got), len(expected), expected)
	}
	for i, p := range got {
		if p.Name != expected[i] {
			t.Fatalf("%s: provider[%d] = %s, want %s", modelName, i, p.Name, expected[i])
		}
	}
}

func providerNames(providers []*config.Provider) []string {
	names := make([]string, len(providers))
	for i, p := range providers {
		names[i] = p.Name
	}
	return names
}

func TestAliasTargetKeywordBehavesLikeDirectKeyword(t *testing.T) {
	p1a := &config.Provider{Name: "p1a", ModelID: "m1", Priority: 1, Format: "openai"}
	p1b := &config.Provider{Name: "p1b", ModelID: "m1", Priority: 1, Format: "openai"}
	p2a := &config.Provider{Name: "p2a", ModelID: "m2", Priority: 2, Format: "openai"}
	p3a := &config.Provider{Name: "p3a", ModelID: "m3", Priority: 3, Format: "openai"}

	e := aliasEngine([]config.ModelRoute{
		{Alias: "gpt-4o", Target: "P1"},
		{Alias: "gpt-4o-mini", Target: "P2up"},
		{Alias: "cheap", Target: "P2down"},
		{Alias: "best", Target: "Max"},
		{Alias: "fast", Target: "Flash"},
	}, p1a, p1b, p2a, p3a)

	// Alias targets are hard selections: identical candidate chains to the
	// client sending the keyword itself.
	assertOrderedNames(t, e, "gpt-4o", "p1a", "p1b")
	assertOrderedNames(t, e, "gpt-4o-mini", "p1a", "p1b", "p2a")
	assertOrderedNames(t, e, "cheap", "p2a", "p3a")
	assertOrderedNames(t, e, "best", "p1a", "p1b")
	assertOrderedNames(t, e, "fast", "p2a", "p3a")
	assertOrderedNames(t, e, "P1", "p1a", "p1b")
	assertOrderedNames(t, e, "P2up", "p1a", "p1b", "p2a")
}

func TestAliasTargetKeywordEmptyBandDoesNotFallBack(t *testing.T) {
	p1 := &config.Provider{Name: "p1", ModelID: "m1", Priority: 1, Format: "openai"}

	e := aliasEngine([]config.ModelRoute{
		{Alias: "gpt-4o", Target: "P9"},
	}, p1)

	if got := e.getOrderedProviders("gpt-4o"); len(got) != 0 {
		t.Errorf("alias to empty band: got %v, want no providers (hard selection)",
			providerNames(got))
	}
}

func TestAliasTargetProviderNameStillPinsAndFallsBack(t *testing.T) {
	p1 := &config.Provider{Name: "p1", ModelID: "m1", Priority: 1, Format: "openai"}
	backup := &config.Provider{Name: "backup", ModelID: "m2", Priority: 2, Format: "openai"}
	p3 := &config.Provider{Name: "p3", ModelID: "m3", Priority: 3, Format: "openai"}

	e := aliasEngine([]config.ModelRoute{
		{Alias: "gpt-4o", Target: "backup"},
	}, p1, backup, p3)

	// Soft pin: the target leads, every remaining provider stays as fallback.
	// The name must not match the keyword grammar (P{n}[up|down]) — keyword
	// interpretation takes precedence.
	assertOrderedNames(t, e, "gpt-4o", "backup", "p1", "p3")
}

func TestAliasTargetKeywordTakesPrecedenceOverProviderName(t *testing.T) {
	namedP1 := &config.Provider{Name: "P1", ModelID: "m1", Priority: 3, Format: "openai"}
	other := &config.Provider{Name: "other", ModelID: "m2", Priority: 1, Format: "openai"}

	e := aliasEngine([]config.ModelRoute{
		{Alias: "gpt-4o", Target: "P1"},
	}, other, namedP1)

	// The keyword interpretation wins even when a provider shares its name:
	// target "P1" selects the priority-1 band, not the provider "P1".
	assertOrderedNames(t, e, "gpt-4o", "other")
}

func TestDefaultRouteTargetKeywordExpands(t *testing.T) {
	p1 := &config.Provider{Name: "p1", ModelID: "m1", Priority: 1, Format: "openai"}
	p2 := &config.Provider{Name: "p2", ModelID: "m2", Priority: 2, Format: "openai"}

	e := aliasEngine([]config.ModelRoute{
		{Alias: "default", Target: "P2down"},
	}, p1, p2)

	// The default route resolves keywords to their band at the default
	// position; remaining providers still follow as fallback.
	assertOrderedNames(t, e, "unknown-model", "p2", "p1")
}

// endToEndEngine wires a full HandleRequest stack against two httptest
// upstreams, one per priority band.
func endToEndEngine(t *testing.T, routes []config.ModelRoute, p1URL, p2URL string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		Global:      config.GlobalConfig{CBThreshold: 3, CBCooldown: 10, CBSkipRequests: 1},
		ModelRoutes: routes,
		Providers: []config.Provider{
			{Name: "p1a", ModelID: "m1", APIKey: "k", BaseURL: p1URL + "/v1", Priority: 1, Format: "openai", Timeout: 5},
			{Name: "p2a", ModelID: "m2", APIKey: "k", BaseURL: p2URL + "/v1", Priority: 2, Format: "openai", Timeout: 5},
		},
	}
	e := NewEngine(cfg, "", io.Discard, io.Discard, stats.New(""))
	router := gin.New()
	router.Use(middleware.DetectFormat())
	router.POST("/chat/completions", e.HandleRequest)
	return router
}

func postModel(router *gin.Engine, model string) int {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/chat/completions",
		strings.NewReader(fmt.Sprintf(`{"model":%q}`, model)))
	router.ServeHTTP(recorder, request)
	return recorder.Code
}

// TestAliasKeywordTargetDoesNotDegradeAcrossBandsEndToEnd proves through the
// full request path that a keyword alias is a hard selection: when the pinned
// band's upstream fails, the lower-priority provider is never contacted.
func TestAliasKeywordTargetDoesNotDegradeAcrossBandsEndToEnd(t *testing.T) {
	var p2Hits atomic.Int32
	p1Upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer p1Upstream.Close()
	p2Upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer p2Upstream.Close()

	router := endToEndEngine(t,
		[]config.ModelRoute{{Alias: "gpt-4o", Target: "P1"}},
		p1Upstream.URL, p2Upstream.URL)

	if status := postModel(router, "gpt-4o"); status != http.StatusServiceUnavailable {
		t.Fatalf("gpt-4o status = %d, want 503", status)
	}
	if hits := p2Hits.Load(); hits != 0 {
		t.Fatalf("P2 upstream received %d requests, want 0 (keyword targets must not degrade across bands)", hits)
	}

	// Control: direct P2 keyword still reaches the P2 provider.
	if status := postModel(router, "P2"); status != http.StatusOK {
		t.Fatalf("P2 status = %d, want 200", status)
	}
	if hits := p2Hits.Load(); hits != 1 {
		t.Fatalf("P2 upstream hits = %d, want 1", hits)
	}
}

// TestAliasProviderNameTargetFallsBackEndToEnd proves the soft-pin path
// through the full request stack: the pinned provider's failure degrades to
// the next priority group.
func TestAliasProviderNameTargetFallsBackEndToEnd(t *testing.T) {
	var p2Hits atomic.Int32
	p1Upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer p1Upstream.Close()
	p2Upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p2Hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
	}))
	defer p2Upstream.Close()

	// The pinned provider (P1) fails; the alias chain must fall through to P2.
	router := endToEndEngine(t,
		[]config.ModelRoute{{Alias: "gpt-4o", Target: "p1a"}},
		p1Upstream.URL, p2Upstream.URL)

	if status := postModel(router, "gpt-4o"); status != http.StatusOK {
		t.Fatalf("gpt-4o status = %d, want 200 via P2 fallback", status)
	}
	if hits := p2Hits.Load(); hits != 1 {
		t.Fatalf("P2 upstream hits = %d, want 1 (soft-pin fallback)", hits)
	}
}
