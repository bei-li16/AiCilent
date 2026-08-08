package server

import (
	"path/filepath"
	"testing"
)

func TestResolveLogPathRelativeToConfig(t *testing.T) {
	configPath := filepath.Join("release", "config", "providers.yaml")
	got := resolveLogPath("proxy.log", configPath)
	want := filepath.Join("release", "config", "proxy.log")
	if got != want {
		t.Fatalf("resolveLogPath() = %q, want %q", got, want)
	}
}

func TestResolveLogPathPreservesAbsoluteAndEmptyPaths(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "proxy.log")
	if got := resolveLogPath(abs, "config/providers.yaml"); got != abs {
		t.Fatalf("absolute path changed to %q", got)
	}
	if got := resolveLogPath("", "config/providers.yaml"); got != "" {
		t.Fatalf("empty path changed to %q", got)
	}
}
