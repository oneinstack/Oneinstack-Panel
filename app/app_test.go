package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeBasePath(t *testing.T) {
	got := normalizeBasePath(filepath.Join("tmp", "..", "data"))
	want := "data" + string(os.PathSeparator)
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestResolveBasePathFromEnvironment(t *testing.T) {
	configuredPath := filepath.Join(t.TempDir(), "panel-data")
	t.Setenv("ONEINSTACK_BASE_PATH", configuredPath)

	got := resolveBasePath()
	want := normalizeBasePath(configuredPath)
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
