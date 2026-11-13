package router

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"ai-proxy/internal/config"
	"ai-proxy/internal/stats"
)

func TestOpenAIModelIDsFollowReloadedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "providers.yaml")
	writeConfig := func(model, alias string) {
		t.Helper()
		body := "providers:\n  - name: fixture\n    model_id: " + model + "\n" +
			"    api_key: fixture-key\n    base_url: http://127.0.0.1:1/v1\n    format: openai\n    priority: 2\n" +
			"model_routes:\n  - alias: " + alias + "\n    target: P2\n"
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("old-model", "old-route")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(cfg, path, io.Discard, io.Discard, stats.New(""))
	if got := engine.OpenAIModelIDs(); !slices.Equal(got, []string{"Max", "Medium", "P2", "old-model", "old-route"}) {
		t.Fatalf("initial list = %v", got)
	}
	writeConfig("new-model", "new-route")
	if err := engine.reloadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := engine.OpenAIModelIDs(); !slices.Equal(got, []string{"Max", "Medium", "P2", "new-model", "new-route"}) {
		t.Fatalf("reloaded list = %v", got)
	}
}
