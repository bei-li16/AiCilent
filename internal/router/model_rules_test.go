package router

import (
	"encoding/json"
	"testing"

	"ai-proxy/internal/config"
)

func TestApplyModelDefaultsUsesTimeoutInternally(t *testing.T) {
	body := []byte(`{"model":"flash","temperature":0.7}`)
	rules := []config.ModelRule{{
		Model: "flash",
		Defaults: map[string]interface{}{
			"temperature": 0.2,
			"max_tokens":  256,
			"timeout":     4,
		},
	}}

	modified, timeout := applyModelDefaults(body, "flash", rules)
	if timeout != 4 {
		t.Fatalf("timeout = %d, want 4", timeout)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(modified, &got); err != nil {
		t.Fatal(err)
	}
	if got["temperature"] != 0.7 {
		t.Fatalf("existing temperature was overwritten: %#v", got["temperature"])
	}
	if got["max_tokens"] != float64(256) {
		t.Fatalf("max_tokens = %#v, want 256", got["max_tokens"])
	}
	if _, exists := got["timeout"]; exists {
		t.Fatal("model timeout must not be sent as an upstream JSON parameter")
	}
}

func TestApplyModelDefaultsIgnoresOtherModels(t *testing.T) {
	body := []byte(`{"model":"other"}`)
	modified, timeout := applyModelDefaults(body, "other", []config.ModelRule{{
		Model: "flash", Defaults: map[string]interface{}{"timeout": 4},
	}})
	if string(modified) != string(body) || timeout != 0 {
		t.Fatalf("unexpected modification: body=%s timeout=%d", modified, timeout)
	}
}

func TestApplyModelDefaultsDoesNotOverrideExplicitTimeout(t *testing.T) {
	body := []byte(`{"model":"flash","timeout":2}`)
	modified, timeout := applyModelDefaults(body, "flash", []config.ModelRule{{
		Model: "flash", Defaults: map[string]interface{}{"timeout": 4},
	}})
	if timeout != 2 || string(modified) != string(body) {
		t.Fatalf("explicit timeout was overridden: body=%s timeout=%d", modified, timeout)
	}
}
