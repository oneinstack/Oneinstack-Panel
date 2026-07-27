package filemanager

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTrashCleanerRemovesExpiredEntries(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "expired.txt"), []byte("expired"), 0644); err != nil {
		t.Fatal(err)
	}
	entry, err := manager.MoveToTrash("/expired.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	entry.DeletedAt = time.Now().UTC().AddDate(0, 0, -2)
	if err := manager.root.Remove(trashMetadataPath(entry.ID)); err != nil {
		t.Fatal(err)
	}
	if err := manager.writeTrashMetadata(entry); err != nil {
		t.Fatal(err)
	}

	cleaner, err := NewTrashCleaner(rootPath, 1, "0 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := cleaner.RunNow()
	if err != nil {
		t.Fatalf("RunNow() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("RunNow() deleted = %d, want 1", deleted)
	}
	if _, err := manager.RestoreTrash(entry.ID); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expired entry is still restorable: %v", err)
	}
}

func TestTrashCleanerConfigurationAndLifecycle(t *testing.T) {
	rootPath := t.TempDir()
	invalid := []struct {
		root      string
		retention int
		schedule  string
	}{
		{root: "", retention: 30, schedule: "0 3 * * *"},
		{root: rootPath, retention: 0, schedule: "0 3 * * *"},
		{root: rootPath, retention: 30, schedule: "not a cron"},
	}
	for _, test := range invalid {
		if _, err := NewTrashCleaner(test.root, test.retention, test.schedule); err == nil {
			t.Fatalf("NewTrashCleaner(%q, %d, %q) should fail", test.root, test.retention, test.schedule)
		}
	}

	cleaner, err := NewTrashCleaner(rootPath, 30, "@daily")
	if err != nil {
		t.Fatal(err)
	}
	cleaner.Start()
	stopContext, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := cleaner.Stop(stopContext); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
}
