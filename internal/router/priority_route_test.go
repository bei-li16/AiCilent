package router

import (
	"testing"

	"ai-proxy/internal/config"
)

func testEngine(providers ...*config.Provider) *Engine {
	cfg := &config.Config{Providers: make([]config.Provider, len(providers))}
	for i, p := range providers {
		cfg.Providers[i] = *p
	}
	return &Engine{cfg: cfg}
}

func TestPriorityRouting(t *testing.T) {
	p1a := &config.Provider{Name: "p1a", ModelID: "m1", Priority: 1, Format: "openai"}
	p1b := &config.Provider{Name: "p1b", ModelID: "m1", Priority: 1, Format: "openai"}
	p2a := &config.Provider{Name: "p2a", ModelID: "m2", Priority: 2, Format: "openai"}
	p2b := &config.Provider{Name: "p2b", ModelID: "m2", Priority: 2, Format: "openai"}
	p3a := &config.Provider{Name: "p3a", ModelID: "m3", Priority: 3, Format: "openai"}

	e := testEngine(p1a, p1b, p2a, p2b, p3a)

	assertNames := func(t *testing.T, modelName string, expected ...string) {
		t.Helper()
		got := e.getOrderedProviders(modelName)
		if len(got) != len(expected) {
			t.Errorf("%s: got %d providers, want %d", modelName, len(got), len(expected))
			return
		}
		for i, p := range got {
			if p.Name != expected[i] {
				t.Errorf("%s: provider[%d] = %s, want %s", modelName, i, p.Name, expected[i])
			}
		}
	}

	assertNames(t, "P1", "p1a", "p1b")
	assertNames(t, "P2", "p2a", "p2b")
	assertNames(t, "P3", "p3a")
	assertNames(t, "P2up", "p1a", "p1b", "p2a", "p2b")
	assertNames(t, "P2down", "p2a", "p2b", "p3a")
	assertNames(t, "P3down", "p3a")
	assertNames(t, "P3up", "p1a", "p1b", "p2a", "p2b", "p3a")
	assertNames(t, "Max", "p1a", "p1b")
	assertNames(t, "Flash", "p2a", "p2b", "p3a")
	assertNames(t, "Medium", "p1a", "p1b", "p2a", "p2b", "p3a")
}

func TestPriorityRoutingEmptyGroup(t *testing.T) {
	p1 := &config.Provider{Name: "p1", ModelID: "m1", Priority: 1, Format: "openai"}
	p3 := &config.Provider{Name: "p3", ModelID: "m3", Priority: 3, Format: "openai"}

	e := testEngine(p1, p3)

	if got := e.getOrderedProviders("P2"); len(got) != 0 {
		t.Errorf("P2 with no P2 providers: got %d, want 0", len(got))
	}
	if got := e.getOrderedProviders("P2up"); len(got) != 1 {
		t.Errorf("P2up with P1,P3: got %d, want 1 (only P1)", len(got))
	}
	if got := e.getOrderedProviders("P2down"); len(got) != 1 {
		t.Errorf("P2down with P1,P3: got %d, want 1 (only P3)", len(got))
	}
}

func TestModeFilter(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Max", "highest priority only"},
		{"Flash", "skip highest priority group"},
		{"Medium", "all priorities"},
		{"P1", "P1 only"},
		{"P2", "P2 only"},
		{"P2up", "P1..P2"},
		{"P3down", "P3..max"},
		{"gpt-4o", "model match"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := modeFilter(tt.input); got != tt.want {
				t.Errorf("modeFilter(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestMinPriority(t *testing.T) {
	e := testEngine(
		&config.Provider{Name: "a", Priority: 2},
		&config.Provider{Name: "b", Priority: 5},
	)
	if e.minPriority() != 2 {
		t.Errorf("minPriority() = %d, want 2", e.minPriority())
	}

	eEmpty := &Engine{cfg: &config.Config{}}
	if eEmpty.minPriority() != 0 {
		t.Errorf("minPriority() empty = %d, want 0", eEmpty.minPriority())
	}
}
