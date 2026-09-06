package config

import "testing"

func TestParsePriorityKeyword(t *testing.T) {
	tests := []struct {
		input    string
		priority int
		mode     string
		ok       bool
	}{
		{"P1", 1, "only", true},
		{"P2", 2, "only", true},
		{"P10", 10, "only", true},
		{"P2up", 2, "up", true},
		{"P3down", 3, "down", true},
		{"p2", 2, "only", true},
		{"p2UP", 2, "up", true},
		{"p2DOWN", 2, "down", true},
		{"", 0, "", false},
		{"P", 0, "", false},
		{"P0", 0, "", false},
		{"Pabc", 0, "", false},
		{"P2x", 0, "", false},
		{"Max", 0, "", false},
		{"gpt-4o", 0, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			pri, mode, ok := ParsePriorityKeyword(tt.input)
			if pri != tt.priority || mode != tt.mode || ok != tt.ok {
				t.Errorf("ParsePriorityKeyword(%q) = (%d, %q, %v), want (%d, %q, %v)",
					tt.input, pri, mode, ok, tt.priority, tt.mode, tt.ok)
			}
		})
	}
}

func TestIsRouteKeyword(t *testing.T) {
	valid := []string{"Max", "Flash", "Medium", "P1", "P10", "P2up", "P3down", "p2UP", "p2DOWN"}
	for _, s := range valid {
		if !IsRouteKeyword(s) {
			t.Errorf("IsRouteKeyword(%q) = false, want true", s)
		}
	}
	invalid := []string{"", "P", "P0", "Pabc", "P2x", "max", "flash", "medium", "gpt-4o", "default"}
	for _, s := range invalid {
		if IsRouteKeyword(s) {
			t.Errorf("IsRouteKeyword(%q) = true, want false", s)
		}
	}
}
