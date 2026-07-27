package filemanager

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func newTestManager(t *testing.T) (*Manager, string) {
	t.Helper()

	rootPath := t.TempDir()
	manager, err := New(rootPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return manager, rootPath
}

func TestRelativeVirtualPaths(t *testing.T) {
	manager, _ := newTestManager(t)

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "root", input: "/", want: "."},
		{name: "empty root", input: "", want: "."},
		{name: "child", input: "/sites/example", want: "sites/example"},
		{name: "normalizes duplicate separators", input: "//sites//example/", want: "sites/example"},
		{name: "parent from root", input: "../etc", wantErr: true},
		{name: "parent in middle", input: "/sites/../etc", wantErr: true},
		{name: "parent only", input: "..", wantErr: true},
		{name: "windows separator", input: `sites\example`, wantErr: true},
		{name: "nul", input: "sites/\x00example", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := manager.Relative(test.input)
			if test.wantErr {
				if !errors.Is(err, ErrInvalidPath) {
					t.Fatalf("Relative(%q) error = %v, want ErrInvalidPath", test.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Relative(%q) error = %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("Relative(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"index.html", "站点配置.conf", ".env"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) error = %v", name, err)
		}
	}

	invalid := []string{"", ".", "..", "../secret", "a/b", `a\b`, "line\nbreak"}
	for _, name := range invalid {
		if !errors.Is(ValidateName(name), ErrInvalidName) {
			t.Errorf("ValidateName(%q) should return ErrInvalidName", name)
		}
	}
}

func TestManagerFileLifecycle(t *testing.T) {
	manager, rootPath := newTestManager(t)

	if err := manager.MkdirAll("/sites/example", 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := manager.CreateFile("/sites/example/index.html", 0644); err != nil {
		t.Fatalf("CreateFile() error = %v", err)
	}
	if err := manager.WriteExistingFile("/sites/example/index.html", []byte("hello")); err != nil {
		t.Fatalf("WriteExistingFile() error = %v", err)
	}

	file, relative, err := manager.Open("/sites/example/index.html")
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer file.Close()

	content, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(content) != "hello" {
		t.Fatalf("content = %q, want hello", content)
	}
	if relative != "sites/example/index.html" {
		t.Fatalf("relative = %q", relative)
	}

	if _, err := os.Stat(filepath.Join(rootPath, "sites", "example", "index.html")); err != nil {
		t.Fatalf("expected file below root: %v", err)
	}

	if err := manager.RemoveAll("/sites"); err != nil {
		t.Fatalf("RemoveAll() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "sites")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sites should be removed, error = %v", err)
	}
	if !errors.Is(manager.RemoveAll("/"), ErrRootOperation) {
		t.Fatal("RemoveAll(/) should reject deleting the authorized root")
	}
}

func TestManagerRejectsSymlinkEscape(t *testing.T) {
	manager, rootPath := newTestManager(t)
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	linkPath := filepath.Join(rootPath, "escape")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlink is not supported: %v", err)
	}

	if file, _, err := manager.Open("/escape"); err == nil {
		file.Close()
		t.Fatal("Open() followed a symlink outside the authorized root")
	}

	if err := manager.RemoveAll("/escape"); err != nil {
		t.Fatalf("RemoveAll(symlink) error = %v", err)
	}
	if content, err := os.ReadFile(outsidePath); err != nil || string(content) != "secret" {
		t.Fatalf("outside target was modified: content=%q error=%v", content, err)
	}
}

func TestPhysicalAbsolutePathCannotEscapeRoot(t *testing.T) {
	manager, _ := newTestManager(t)
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}

	file, _, err := manager.Open(outsidePath)
	if err == nil {
		file.Close()
		t.Fatalf("physical absolute path %q escaped the virtual root", outsidePath)
	}
}

func TestManagerAllowsRelativeSymlinkWithinRoot(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "target.txt"), []byte("inside"), 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Symlink("target.txt", filepath.Join(rootPath, "link.txt")); err != nil {
		t.Skipf("symlink is not supported: %v", err)
	}

	file, _, err := manager.Open("/link.txt")
	if err != nil {
		t.Fatalf("Open() internal link error = %v", err)
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil || string(content) != "inside" {
		t.Fatalf("content=%q error=%v", content, err)
	}
}
