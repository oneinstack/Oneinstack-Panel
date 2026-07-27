package filemanager

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrashFileLifecycle(t *testing.T) {
	manager, rootPath := newTestManager(t)
	originalPath := filepath.Join(rootPath, "sites", "example", "index.html")
	if err := os.MkdirAll(filepath.Dir(originalPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	entry, err := manager.MoveToTrash("/sites/example/index.html", "admin")
	if err != nil {
		t.Fatalf("MoveToTrash() error = %v", err)
	}
	if entry.OriginalPath != "/sites/example/index.html" || entry.DeletedBy != "admin" {
		t.Fatalf("unexpected trash entry: %+v", entry)
	}
	if _, err := os.Stat(originalPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("source still exists after delete: %v", err)
	}

	entries, err := manager.ListTrash()
	if err != nil {
		t.Fatalf("ListTrash() error = %v", err)
	}
	if len(entries) != 1 || entries[0].ID != entry.ID {
		t.Fatalf("trash entries = %+v", entries)
	}

	restored, err := manager.RestoreTrash(entry.ID)
	if err != nil {
		t.Fatalf("RestoreTrash() error = %v", err)
	}
	if restored.OriginalPath != entry.OriginalPath {
		t.Fatalf("restored entry = %+v", restored)
	}
	content, err := os.ReadFile(originalPath)
	if err != nil || string(content) != "hello" {
		t.Fatalf("restored content=%q error=%v", content, err)
	}
	entries, err = manager.ListTrash()
	if err != nil || len(entries) != 0 {
		t.Fatalf("trash should be empty after restore: entries=%+v error=%v", entries, err)
	}
}

func TestTrashRestoreDoesNotOverwriteConflict(t *testing.T) {
	manager, rootPath := newTestManager(t)
	originalPath := filepath.Join(rootPath, "config.ini")
	if err := os.WriteFile(originalPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.MoveToTrash("/config.ini", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.RestoreTrash(entry.ID); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("RestoreTrash() error = %v, want fs.ErrExist", err)
	}
	content, err := os.ReadFile(originalPath)
	if err != nil || string(content) != "new" {
		t.Fatalf("conflicting file was overwritten: content=%q error=%v", content, err)
	}
	entries, err := manager.ListTrash()
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash entry disappeared after conflict: entries=%+v error=%v", entries, err)
	}
}

func TestTrashDirectoryPreservesSymlinkWithoutFollowingIt(t *testing.T) {
	manager, rootPath := newTestManager(t)
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	directoryPath := filepath.Join(rootPath, "project")
	if err := os.Mkdir(directoryPath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directoryPath, "inside.txt"), []byte("inside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(directoryPath, "outside-link")); err != nil {
		t.Skipf("symlink is not supported: %v", err)
	}

	entry, err := manager.MoveToTrash("/project", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RestoreTrash(entry.ID); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(filepath.Join(directoryPath, "outside-link"))
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("restored link is not a symlink: info=%v error=%v", linkInfo, err)
	}
	content, err := os.ReadFile(outsidePath)
	if err != nil || string(content) != "secret" {
		t.Fatalf("outside target changed: content=%q error=%v", content, err)
	}
}

func TestTrashPermanentDeleteAndEmpty(t *testing.T) {
	manager, rootPath := newTestManager(t)
	for _, name := range []string{"one.txt", "two.txt"} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	first, err := manager.MoveToTrash("/one.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.MoveToTrash("/two.txt", ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.DeleteTrashPermanently(first.ID); err != nil {
		t.Fatalf("DeleteTrashPermanently() error = %v", err)
	}
	if _, err := manager.RestoreTrash(first.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted entry can still be restored: %v", err)
	}

	deleted, err := manager.EmptyTrash()
	if err != nil {
		t.Fatalf("EmptyTrash() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("EmptyTrash() deleted = %d, want 1", deleted)
	}
	entries, err := manager.ListTrash()
	if err != nil || len(entries) != 0 {
		t.Fatalf("trash is not empty: entries=%+v error=%v", entries, err)
	}
}

func TestCleanupTrashBefore(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.MoveToTrash("/old.txt", ""); err != nil {
		t.Fatal(err)
	}

	deleted, err := manager.CleanupTrashBefore(time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("CleanupTrashBefore() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
}

func TestTrashInternalDirectoryIsHiddenAndReserved(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "file.txt"), []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.MoveToTrash("/file.txt", ""); err != nil {
		t.Fatal(err)
	}

	entries, _, err := manager.ReadDir("/")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == internalDirectoryName {
			t.Fatal("internal trash directory is visible in the file listing")
		}
	}
	if _, err := manager.Relative("/" + internalDirectoryName); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("Relative(internal) error = %v, want ErrReservedPath", err)
	}
	if _, err := manager.OpenRelative(internalDirectoryName); !errors.Is(err, ErrReservedPath) {
		t.Fatalf("OpenRelative(internal) error = %v, want ErrReservedPath", err)
	}
}

func TestTrashRejectsUnsafeInternalDirectory(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := t.TempDir()
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, internalDirectoryName)); err != nil {
		t.Skipf("symlink is not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "file.txt"), []byte("file"), 0644); err != nil {
		t.Fatal(err)
	}
	manager, err := New(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	if _, err := manager.MoveToTrash("/file.txt", ""); !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("MoveToTrash() error = %v, want ErrInvalidPath", err)
	}
	outsideEntries, err := os.ReadDir(outsidePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(outsideEntries) != 0 {
		t.Fatalf("unsafe trash symlink wrote outside root: %v", outsideEntries)
	}
}

func TestRestoreRejectsInvalidTrashID(t *testing.T) {
	manager, _ := newTestManager(t)
	if _, err := manager.RestoreTrash("../metadata"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("RestoreTrash() error = %v, want ErrInvalidName", err)
	}
}
