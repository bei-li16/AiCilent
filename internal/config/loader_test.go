package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRemovedConcurrencySettingIsIgnoredAndNotSerialized(t *testing.T) {
	cfg, err := Load(writeConfigFile(t, validProvider+"    max_concurrent: 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	yamlData, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(yamlData), "max_concurrent") || strings.Contains(string(jsonData), "max_concurrent") {
		t.Fatal("removed concurrency setting was serialized")
	}
}

func TestLoadReasoningEffortOnNone(t *testing.T) {
	for _, tc := range []struct {
		policy, wantError string
	}{
		{"low", ""},
		{"max", ""},
		{"", ""},
		{"passthrough", "global reasoning_effort_on_none must be"},
		{"lowest", "global reasoning_effort_on_none must be"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			input := "global:\n  reasoning_effort_on_none: " + tc.policy + "\n" + validProvider
			cfg, err := Load(writeConfigFile(t, input))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("Load error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Global.ReasoningEffortOnNone; got != tc.policy {
				t.Fatalf("policy = %q, want %q", got, tc.policy)
			}
		})
	}
}

const validProvider = `
providers:
  - name: a
    api_key: k
    base_url: https://api.example.com/v1
    priority: 1
    format: openai
`

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "providers.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAcceptsProviderNameAndKeywordTargets(t *testing.T) {
	path := writeConfigFile(t, validProvider+`
model_routes:
  - alias: gpt-4o
    target: a
  - alias: best
    target: P1
  - alias: fast
    target: Flash
  - alias: cheap
    target: P2down
  - alias: default
    target: a
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.ModelRoutes) != 5 {
		t.Fatalf("routes = %d, want 5", len(cfg.ModelRoutes))
	}
}

func TestLoadRejectsUnknownRouteTarget(t *testing.T) {
	path := writeConfigFile(t, validProvider+`
model_routes:
  - alias: gpt-4o
    target: no-such-provider
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "matches no provider name or routing keyword") {
		t.Fatalf("Load err = %v, want unknown-target error", err)
	}
}

func TestLoadRejectsDuplicateRouteAlias(t *testing.T) {
	path := writeConfigFile(t, validProvider+`
model_routes:
  - alias: gpt-4o
    target: a
  - alias: gpt-4o
    target: P1
`)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "duplicate model route alias") {
		t.Fatalf("Load err = %v, want duplicate-alias error", err)
	}
}

func TestLoadRejectsEmptyRouteAliasAndTarget(t *testing.T) {
	path := writeConfigFile(t, validProvider+`
model_routes:
  - target: a
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "alias is required") {
		t.Fatalf("Load err = %v, want missing-alias error", err)
	}

	path = writeConfigFile(t, validProvider+`
model_routes:
  - alias: gpt-4o
`)
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("Load err = %v, want missing-target error", err)
	}
}

// TestLauncherDefaultTemplatePassesValidation guards the GUI bootstrap
// template: cmd/launcher only builds with -tags gui (needs CGO), so without
// this check template changes would evade validation on machines without a
// C toolchain.
func TestLauncherDefaultTemplatePassesValidation(t *testing.T) {
	const path = "../../cmd/launcher/default_providers.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("launcher template not found: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("cmd/launcher/default_providers.yaml fails validation: %v", err)
	}
}

// TestExampleConfigPassesValidation keeps the copy-paste starter config
// loadable: it is the first file every new user runs through Load.
func TestExampleConfigPassesValidation(t *testing.T) {
	const path = "../../config/providers.example.yaml"
	if _, err := os.Stat(path); err != nil {
		t.Skipf("example config not found: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("config/providers.example.yaml fails validation: %v", err)
	}
}

func TestDocumentedTemplateDefaults(t *testing.T) {
	for _, path := range []string{"../../config/providers.example.yaml", "../../cmd/launcher/default_providers.yaml"} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var raw Config
			decoder := yaml.NewDecoder(bytes.NewReader(data))
			decoder.KnownFields(true)
			if err := decoder.Decode(&raw); err != nil {
				t.Fatalf("template contains unknown or invalid fields: %v", err)
			}
			cfg, err := Parse(data, ".yaml")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Global.MaxStreamMinutes != 15 || cfg.Global.ReasoningEffortOnNone != "" ||
				cfg.Global.RateLimitCooldown != 0 || cfg.Global.LogFile != "../proxy.log" {
				t.Fatalf("documented template defaults changed: %+v", cfg.Global)
			}
			for _, p := range cfg.Providers {
				if p.Timeout != 60 || p.Retry.MaxRetries != 3 || p.RateLimit.Enabled {
					t.Fatalf("provider %s differs from documented defaults", p.Name)
				}
			}
		})
	}
}

func TestExampleIncludesBothProtocolsForEachDocumentedPriority(t *testing.T) {
	cfg, err := Load("../../config/providers.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{1: "kimi-k3", 2: "glm-5.3", 3: "deepseek-flash"}
	seen := make(map[int]map[string]bool)
	for _, p := range cfg.Providers {
		if p.ModelID != want[p.Priority] {
			t.Fatalf("unexpected P%d model %s", p.Priority, p.ModelID)
		}
		if seen[p.Priority] == nil {
			seen[p.Priority] = make(map[string]bool)
		}
		seen[p.Priority][p.Format] = true
	}
	for priority := range want {
		if !seen[priority]["openai"] || !seen[priority]["anthropic"] {
			t.Fatalf("P%d is missing a protocol in the example", priority)
		}
	}
}

func TestDocumentedOmittedAndZeroRetryDefaults(t *testing.T) {
	for _, retryYAML := range []string{"", "    retry:\n      max_retries: 0\n"} {
		cfg, err := Parse([]byte(validProvider+retryYAML), ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Providers[0].Retry.MaxRetries != 3 || cfg.Global.MaxStreamMinutes != 3 {
			t.Fatal("omitted/zero retry or omitted stream duration no longer matches documented defaults")
		}
	}
}
