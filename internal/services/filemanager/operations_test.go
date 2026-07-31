package filemanager

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyMoveRenameAndArchive(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, "source", "nested"), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source", "nested", "config.txt"), []byte("hello"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(rootPath, "backup"), 0755); err != nil {
		t.Fatal(err)
	}
	manager, err := New(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	copied, err := manager.Copy("/source", "/backup", "copied")
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.Path != "/backup/copied" || copied.Bytes != 5 || copied.Entries != 3 {
		t.Fatalf("unexpected copy result: %+v", copied)
	}
	content, err := os.ReadFile(filepath.Join(rootPath, "backup", "copied", "nested", "config.txt"))
	if err != nil || string(content) != "hello" {
		t.Fatalf("copied content=%q err=%v", content, err)
	}
	if _, err := manager.Copy("/source", "/backup", "copied"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("copy should refuse overwrite, got %v", err)
	}

	renamed, err := manager.Rename("/backup/copied", "renamed")
	if err != nil || renamed.Path != "/backup/renamed" {
		t.Fatalf("rename result=%+v err=%v", renamed, err)
	}
	moved, err := manager.Move("/backup/renamed", "/", "moved")
	if err != nil || moved.Path != "/moved" {
		t.Fatalf("move result=%+v err=%v", moved, err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, "backup", "renamed")); !os.IsNotExist(err) {
		t.Fatalf("old move path still exists: %v", err)
	}

	archived, err := manager.Archive("/moved", "/", "moved.tar.gz")
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.Path != "/moved.tar.gz" || archived.Entries != 3 || archived.Bytes != 5 {
		t.Fatalf("unexpected archive result: %+v", archived)
	}
	archiveFile, err := os.Open(filepath.Join(rootPath, "moved.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer archiveFile.Close()
	gzipReader, err := gzip.NewReader(archiveFile)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	found := false
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatal(nextErr)
		}
		if header.Name == "moved/nested/config.txt" {
			data, err := io.ReadAll(tarReader)
			if err != nil {
				t.Fatal(err)
			}
			found = string(data) == "hello"
		}
	}
	if !found {
		t.Fatal("archive did not contain expected regular file")
	}
}

func TestCopyAndArchiveRejectSymbolicLinks(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, "target.txt"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(rootPath, "link.txt")); err != nil {
		t.Fatal(err)
	}
	manager, err := New(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	if _, err := manager.Copy("/link.txt", "/", "copy.txt"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("copy symlink error=%v, want ErrUnsupportedType", err)
	}
	if _, err := manager.Archive("/link.txt", "/", "link.tar.gz"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("archive symlink error=%v, want ErrUnsupportedType", err)
	}
}
