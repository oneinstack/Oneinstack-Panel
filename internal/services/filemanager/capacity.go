package filemanager

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sync"

	"github.com/shirou/gopsutil/v4/disk"
)

var (
	ErrQuotaExceeded     = errors.New("file root quota exceeded")
	ErrInsufficientSpace = errors.New("insufficient disk space")
)

type CapacityPolicy struct {
	QuotaBytes   int64
	MinFreeBytes int64
}

type CapacityStatus struct {
	UsedBytes          int64 `json:"usedBytes"`
	QuotaBytes         int64 `json:"quotaBytes"`
	DiskTotalBytes     int64 `json:"diskTotalBytes"`
	DiskAvailableBytes int64 `json:"diskAvailableBytes"`
	MinFreeBytes       int64 `json:"minFreeBytes"`
	ReservedBytes      int64 `json:"reservedBytes"`
	WritableBytes      int64 `json:"writableBytes"`
	EntryCount         int64 `json:"entryCount"`
}

type CapacityReservation struct {
	rootPath string
	bytes    int64
	once     sync.Once
}

var capacityReservations = struct {
	sync.Mutex
	byRoot map[string]int64
}{
	byRoot: make(map[string]int64),
}

func (m *Manager) Capacity(policy CapacityPolicy) (CapacityStatus, error) {
	if err := validateCapacityPolicy(policy); err != nil {
		return CapacityStatus{}, err
	}
	capacityReservations.Lock()
	defer capacityReservations.Unlock()
	return m.capacityLocked(policy, capacityReservations.byRoot[m.rootPath])
}

func (m *Manager) ReserveCapacity(bytes int64, policy CapacityPolicy) (*CapacityReservation, CapacityStatus, error) {
	if bytes < 0 {
		return nil, CapacityStatus{}, ErrInvalidPath
	}
	if err := validateCapacityPolicy(policy); err != nil {
		return nil, CapacityStatus{}, err
	}

	capacityReservations.Lock()
	defer capacityReservations.Unlock()
	reserved := capacityReservations.byRoot[m.rootPath]
	status, err := m.capacityLocked(policy, reserved)
	if err != nil {
		return nil, CapacityStatus{}, err
	}
	if policy.QuotaBytes > 0 && bytes > remaining(policy.QuotaBytes, status.UsedBytes+reserved) {
		return nil, status, fmt.Errorf("%w: need %d bytes, quota writable %d bytes", ErrQuotaExceeded, bytes, remaining(policy.QuotaBytes, status.UsedBytes+reserved))
	}
	if bytes > remaining(status.DiskAvailableBytes, policy.MinFreeBytes+reserved) {
		return nil, status, fmt.Errorf("%w: need %d bytes, disk writable %d bytes", ErrInsufficientSpace, bytes, remaining(status.DiskAvailableBytes, policy.MinFreeBytes+reserved))
	}

	capacityReservations.byRoot[m.rootPath] = reserved + bytes
	status.ReservedBytes = reserved + bytes
	status.WritableBytes = writableBytes(status, policy)
	return &CapacityReservation{rootPath: m.rootPath, bytes: bytes}, status, nil
}

func (reservation *CapacityReservation) Release() {
	if reservation == nil {
		return
	}
	reservation.once.Do(func() {
		capacityReservations.Lock()
		defer capacityReservations.Unlock()
		current := capacityReservations.byRoot[reservation.rootPath]
		if current <= reservation.bytes {
			delete(capacityReservations.byRoot, reservation.rootPath)
			return
		}
		capacityReservations.byRoot[reservation.rootPath] = current - reservation.bytes
	})
}

func (m *Manager) capacityLocked(policy CapacityPolicy, reserved int64) (CapacityStatus, error) {
	used, entries, err := m.scanUsage()
	if err != nil {
		return CapacityStatus{}, err
	}
	usage, err := disk.Usage(m.rootPath)
	if err != nil {
		return CapacityStatus{}, fmt.Errorf("read filesystem capacity: %w", err)
	}
	status := CapacityStatus{
		UsedBytes:          used,
		QuotaBytes:         policy.QuotaBytes,
		DiskTotalBytes:     clampUint64(usage.Total),
		DiskAvailableBytes: clampUint64(usage.Free),
		MinFreeBytes:       policy.MinFreeBytes,
		ReservedBytes:      reserved,
		EntryCount:         entries,
	}
	status.WritableBytes = writableBytes(status, policy)
	return status, nil
}

func (m *Manager) scanUsage() (int64, int64, error) {
	var used int64
	var entries int64
	err := fs.WalkDir(m.root.FS(), ".", func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "." {
			return nil
		}
		entries++
		if entry.Type()&fs.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > math.MaxInt64-used {
			return ErrQuotaExceeded
		}
		used += info.Size()
		return nil
	})
	return used, entries, err
}

func writableBytes(status CapacityStatus, policy CapacityPolicy) int64 {
	writable := remaining(status.DiskAvailableBytes, policy.MinFreeBytes+status.ReservedBytes)
	if policy.QuotaBytes > 0 {
		quotaWritable := remaining(policy.QuotaBytes, status.UsedBytes+status.ReservedBytes)
		if quotaWritable < writable {
			writable = quotaWritable
		}
	}
	return writable
}

func remaining(total, consumed int64) int64 {
	if consumed >= total {
		return 0
	}
	return total - consumed
}

func validateCapacityPolicy(policy CapacityPolicy) error {
	if policy.QuotaBytes < 0 || policy.MinFreeBytes < 0 {
		return ErrInvalidPath
	}
	return nil
}

func clampUint64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}
