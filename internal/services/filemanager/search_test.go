package filemanager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSearchFindsFilesAndDirectoriesByName(t *testing.T) {
	manager, rootPath := newTestManager(t)
	for _, directory := range []string{"sites/alpha", "sites/beta", "logs"} {
		if err := os.MkdirAll(filepath.Join(rootPath, directory), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"sites/alpha/index.html": "alpha",
		"sites/beta/index.php":   "beta",
		"logs/access.log":        "log",
	} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := manager.Search(context.Background(), SearchOptions{
		Path: "/", Query: "INDEX", Type: "file", MaxResults: 20,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if result.Total != 2 || result.Items[0].Path != "/sites/alpha/index.html" ||
		result.Items[1].Path != "/sites/beta/index.php" {
		t.Fatalf("unexpected search result: %+v", result)
	}

	directories, err := manager.Search(context.Background(), SearchOptions{
		Path: "/sites", Query: "alpha", Type: "dir",
	})
	if err != nil {
		t.Fatal(err)
	}
	if directories.Total != 1 || directories.Items[0].Path != "/sites/alpha" {
		t.Fatalf("unexpected directory result: %+v", directories)
	}
}

func TestSearchIsBoundedAndRejectsInvalidInput(t *testing.T) {
	manager, rootPath := newTestManager(t)
	for _, name := range []string{"match-1", "match-2", "match-3"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.Search(context.Background(), SearchOptions{
		Path: "/", Query: "match", MaxResults: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || !result.Truncated {
		t.Fatalf("search limit was not enforced: %+v", result)
	}
	if _, err := manager.Search(context.Background(), SearchOptions{
		Path: "../", Query: "match",
	}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("parent traversal error = %v", err)
	}
	if _, err := manager.Search(context.Background(), SearchOptions{
		Path: "/", Query: "",
	}); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("empty query error = %v", err)
	}
}

func TestSearchDoesNotFollowDirectorySymlinks(t *testing.T) {
	manager, rootPath := newTestManager(t)
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside-secret.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, "linked")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := manager.Search(context.Background(), SearchOptions{
		Path: "/", Query: "outside-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 0 {
		t.Fatalf("search followed a directory symlink: %+v", result)
	}
}
