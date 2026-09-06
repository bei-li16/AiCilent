package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
