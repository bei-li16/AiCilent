//go:build gui

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-proxy/internal/config"

	"gopkg.in/yaml.v3"
)

func TestInitializeConfigCreatesStandaloneRuntimeFiles(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "app", "config", "providers.yaml")
	firstRun, err := initializeConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !firstRun {
		t.Fatal("initializeConfig() firstRun = false, want true")
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config file was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(configPath)), "proxy.log")); err != nil {
		t.Fatalf("log file was not created: %v", err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("created config cannot be loaded: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].APIKey != "CHANGE_ME" {
		t.Fatalf("unexpected first-run provider: %#v", cfg.Providers)
	}
}

func TestInitializeConfigMigratesMissingFieldsWithoutOverwritingValues(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config", "providers.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatal(err)
	}
	oldConfig := []byte(`global:
  listen_addr: ":9090"
providers:
  - name: existing
    model_id: existing-model
    api_key: secret-value
    base_url: https://example.com/v1
    priority: 1
    format: openai
`)
	if err := os.WriteFile(configPath, oldConfig, 0644); err != nil {
		t.Fatal(err)
	}

	firstRun, err := initializeConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if firstRun {
		t.Fatal("initializeConfig() firstRun = true for an existing config")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var got config.Config
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Global.ListenAddr != ":9090" {
		t.Fatalf("listen_addr = %q, want preserved value", got.Global.ListenAddr)
	}
	if got.Global.LogFile != "../proxy.log" || got.Global.DefaultFormat != "openai" {
		t.Fatalf("missing global defaults were not migrated: %#v", got.Global)
	}
	if len(got.Providers) != 1 || got.Providers[0].APIKey != "secret-value" {
		t.Fatalf("existing provider credentials changed: %#v", got.Providers)
	}
	if got.Providers[0].AuthType != "bearer" || got.Providers[0].Timeout != 60 {
		t.Fatalf("provider defaults were not migrated: %#v", got.Providers[0])
	}
	if strings.Contains(string(data), "max_concurrent") {
		t.Fatal("migration reintroduced the removed concurrency setting")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(configPath)), "proxy.log")); err != nil {
		t.Fatalf("migrated config log file was not created: %v", err)
	}
}

func TestInitializeConfigRemovesDeprecatedConcurrencyField(t *testing.T) {
	data, err := os.ReadFile("default_providers.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	providers := yamlMapValue(rootMap(&root), "providers")
	provider := providers.Content[0]
	provider.Content = append(provider.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "max_concurrent"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "2"})
	legacy, err := yaml.Marshal(&root)
	if err != nil {
		t.Fatal(err)
	}
	if !configNeedsMigration(legacy) {
		t.Fatal("deprecated field did not trigger migration")
	}
	path := filepath.Join(t.TempDir(), "config", "providers.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := initializeConfig(path); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updated), "max_concurrent") || configNeedsMigration(updated) {
		t.Fatal("deprecated field survived migration or migration repeats forever")
	}
	cfg, err := config.Load(path)
	if err != nil || cfg.Providers[0].APIKey != "CHANGE_ME" {
		t.Fatalf("migration changed provider credentials or invalidated config: %v", err)
	}
}
