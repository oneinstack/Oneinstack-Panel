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

func TestResolveBasePathFromDevelopmentWorkingDirectory(t *testing.T) {
	workspace := t.TempDir()
	t.Setenv("ONEINSTACK_BASE_PATH", "")
	t.Setenv("GO_ENV", "development")
	previousWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd before chdir: %v", err)
	}
	if err := os.Chdir(workspace); err != nil {
		t.Fatalf("chdir workspace: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previousWorkingDirectory)
	})
	actualWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd after chdir: %v", err)
	}

	got := resolveBasePath()
	want := normalizeBasePath(filepath.Join(actualWorkingDirectory, ".runtime"))
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
