package filemanager

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCapacityReportsUsageAndQuota(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "file.txt"), []byte("1234567890"), 0644); err != nil {
		t.Fatal(err)
	}

	status, err := manager.Capacity(CapacityPolicy{QuotaBytes: 20})
	if err != nil {
		t.Fatalf("Capacity() error = %v", err)
	}
	if status.UsedBytes != 10 {
		t.Fatalf("UsedBytes = %d, want 10", status.UsedBytes)
	}
	if status.QuotaBytes != 20 {
		t.Fatalf("QuotaBytes = %d, want 20", status.QuotaBytes)
	}
	if status.WritableBytes != 10 {
		t.Fatalf("WritableBytes = %d, want 10", status.WritableBytes)
	}
	if status.EntryCount != 1 {
		t.Fatalf("EntryCount = %d, want 1", status.EntryCount)
	}
}

func TestCapacityReservationPreventsConcurrentQuotaBypass(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "file.txt"), []byte("1234567890"), 0644); err != nil {
		t.Fatal(err)
	}
	policy := CapacityPolicy{QuotaBytes: 20}

	first, _, err := manager.ReserveCapacity(8, policy)
	if err != nil {
		t.Fatalf("first ReserveCapacity() error = %v", err)
	}
	defer first.Release()
	if _, _, err := manager.ReserveCapacity(3, policy); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("second ReserveCapacity() error = %v, want ErrQuotaExceeded", err)
	}

	first.Release()
	second, _, err := manager.ReserveCapacity(3, policy)
	if err != nil {
		t.Fatalf("ReserveCapacity() after release error = %v", err)
	}
	second.Release()
}

func TestCapacityEnforcesMinimumFreeSpace(t *testing.T) {
	manager, _ := newTestManager(t)
	policy := CapacityPolicy{MinFreeBytes: math.MaxInt64}
	if _, _, err := manager.ReserveCapacity(1, policy); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("ReserveCapacity() error = %v, want ErrInsufficientSpace", err)
	}
}

func TestCapacityIncludesTrashStorage(t *testing.T) {
	manager, rootPath := newTestManager(t)
	if err := os.WriteFile(filepath.Join(rootPath, "file.txt"), []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := manager.Capacity(CapacityPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.MoveToTrash("/file.txt", ""); err != nil {
		t.Fatal(err)
	}
	after, err := manager.Capacity(CapacityPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if after.UsedBytes <= before.UsedBytes {
		t.Fatalf("trash metadata/data should count toward usage: before=%d after=%d", before.UsedBytes, after.UsedBytes)
	}
}
