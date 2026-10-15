package artifactcleanup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"oneinstack/internal/services/artifactpolicy"
)

const (
	cacheRoot  = "/var/cache/oneinstack/downloads"
	stateRoot  = "/var/lib/oneinstack/components"
	cacheLimit = int64(2 << 30)
)

var archiveComponents = []string{"mysql", "redis", "nginx", "mongodb", "mariadb"}

type Candidate struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"`
	Component  string    `json:"component,omitempty"`
	Path       string    `json:"path"`
	Bytes      int64     `json:"bytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Automatic  bool      `json:"automatic"`
	Eligible   bool      `json:"eligible"`
	ReasonCode string    `json:"reasonCode"`
	Reason     string    `json:"reason"`
	device     uint64
	inode      uint64
	modNano    int64
	fileSize   int64
}

type Space struct {
	TotalBytes     uint64  `json:"totalBytes"`
	AvailableBytes uint64  `json:"availableBytes"`
	AvailableRatio float64 `json:"availableRatio"`
	Warning        bool    `json:"warning"`
}

type Inventory struct {
	Space      Space       `json:"space"`
	Candidates []Candidate `json:"candidates"`
}

type LowSpaceError struct{ Space Space }

func (e *LowSpaceError) Error() string {
	return fmt.Sprintf("HOST_DISK_SPACE_LOW: root filesystem has %d bytes available (%.1f%%); free space before installing a large component", e.Space.AvailableBytes, e.Space.AvailableRatio*100)
}

func (e *LowSpaceError) ErrorCode() string { return "HOST_DISK_SPACE_LOW" }

func EnsureHighDemandSpace(component string) error {
	switch strings.ToLower(strings.TrimSpace(component)) {
	case "mysql", "db", "mariadb", "mongodb", "php":
	default:
		return nil
	}
	space, err := DiskSpace()
	if err != nil {
		return err
	}
	if space.AvailableBytes < 5<<30 {
		return &LowSpaceError{Space: space}
	}
	return nil
}

func DiskSpace() (Space, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs("/", &stat); err != nil {
		return Space{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	available := stat.Bavail * uint64(stat.Bsize)
	ratio := float64(0)
	if total > 0 {
		ratio = float64(available) / float64(total)
	}
	return Space{TotalBytes: total, AvailableBytes: available, AvailableRatio: ratio,
		Warning: available < 5<<30 || ratio < 0.10}, nil
}

func List(now time.Time) (Inventory, error) {
	space, err := DiskSpace()
	if err != nil {
		return Inventory{}, err
	}
	result := Inventory{Space: space, Candidates: make([]Candidate, 0)}
	cache, err := listCache(now)
	if err != nil {
		return result, err
	}
	result.Candidates = append(result.Candidates, cache...)
	for _, component := range archiveComponents {
		archives, listErr := listArchives(component, now)
		if listErr != nil {
			return result, listErr
		}
		result.Candidates = append(result.Candidates, archives...)
	}
	sort.Slice(result.Candidates, func(i, j int) bool {
		if result.Candidates[i].Kind != result.Candidates[j].Kind {
			return result.Candidates[i].Kind < result.Candidates[j].Kind
		}
		return result.Candidates[i].ModifiedAt.Before(result.Candidates[j].ModifiedAt)
	})
	return result, nil
}

func listCache(now time.Time) ([]Candidate, error) {
	entries, err := os.ReadDir(cacheRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	if len(entries) > 10000 {
		return nil, errors.New("cache inventory exceeds safe limit")
	}
	cacheParentSafe := false
	if fd, openErr := openDirectoryNoFollow(cacheRoot); openErr == nil {
		cacheParentSafe = true
		unix.Close(fd)
	}
	cacheRootInfo, err := os.Lstat(cacheRoot)
	if err != nil {
		return nil, err
	}
	cacheRootStat, ok := cacheRootInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("cache root metadata is unavailable")
	}
	result := make([]Candidate, 0, len(entries))
	var total int64
	for _, entry := range entries {
		path := filepath.Join(cacheRoot, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		if !info.Mode().IsRegular() {
			continue
		}
		name := entry.Name()
		// Lock files, signatures and package sidecars are not independent cache payloads.
		if strings.HasSuffix(name, ".lock") || strings.HasSuffix(name, ".asc") || strings.HasSuffix(name, ".meta") || strings.HasSuffix(name, ".verified") {
			continue
		}
		if !managedCacheName(name) {
			continue
		}
		incomplete := strings.Contains(name, ".tmp.") || strings.HasSuffix(name, ".part")
		kind := "download_cache"
		retention := 14 * 24 * time.Hour
		if incomplete {
			kind, retention = "download_partial", 24*time.Hour
		}
		candidate := Candidate{Kind: kind, Path: path, Bytes: allocatedBytes(info), ModifiedAt: info.ModTime()}
		setIdentity(&candidate, info)
		candidate.ID = fingerprint(candidate, info, "")
		candidate.Eligible = true
		candidate.ReasonCode = "MANUAL_CLEANUP"
		candidate.Reason = "Download copy; later online installs may download it again"
		if !cacheParentSafe || candidate.device != uint64(cacheRootStat.Dev) {
			candidate.Eligible = false
			candidate.ReasonCode = "UNSAFE_ARTIFACT_PATH"
			candidate.Reason = "Cache path contains a symbolic link or a mounted file"
		}
		verified := incomplete || verifiedCacheMarker(path, info.ModTime())
		if !verified && candidate.Eligible {
			candidate.ReasonCode = "UNVERIFIED_CACHE"
			candidate.Reason = "Existing cache has no ownership and checksum marker; manual confirmation is required"
		}
		if candidate.Eligible && verified && now.Sub(info.ModTime()) >= retention {
			candidate.Automatic = true
			candidate.ReasonCode = "RETENTION_EXPIRED"
			candidate.Reason = "Cache retention expired"
		}
		result = append(result, candidate)
		if !incomplete && verified && candidate.Eligible && !candidate.Automatic {
			total += candidate.Bytes
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ModifiedAt.Before(result[j].ModifiedAt) })
	if total > cacheLimit {
		for i := range result {
			if result[i].Kind != "download_cache" || result[i].Automatic || !result[i].Eligible || result[i].ReasonCode == "UNVERIFIED_CACHE" {
				continue
			}
			result[i].Automatic = true
			result[i].ReasonCode = "CACHE_CAPACITY"
			result[i].Reason = "Cache exceeds 2 GiB; oldest idle copies are selected"
			total -= result[i].Bytes
			if total <= cacheLimit {
				break
			}
		}
	}
	return result, nil
}

func managedCacheName(name string) bool {
	for _, prefix := range []string{"mysql-", "mariadb-", "MariaDB-", "php-", "libzip-", "openssl-", "fail2ban-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func verifiedCacheMarker(path string, cacheModTime time.Time) bool {
	marker := path + ".verified"
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 128 || info.ModTime().Before(cacheModTime) {
		return false
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		return false
	}
	value := strings.TrimSpace(string(content))
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func listArchives(component string, now time.Time) ([]Candidate, error) {
	root := filepath.Join(componentStateRoot(), component, "removed")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s archives: %w", component, err)
	}
	if len(entries) > 10000 {
		return nil, errors.New("archive inventory exceeds safe limit")
	}
	latest := ""
	for _, entry := range entries {
		if entry.IsDir() && validArchiveName(entry.Name()) && entry.Name() > latest {
			latest = entry.Name()
		}
	}
	result := make([]Candidate, 0, len(entries))
	retainedMongoDB := ""
	if component == "mongodb" {
		markerPath := filepath.Join(componentStateRoot(), component, "retained-data")
		if markerInfo, markerErr := os.Lstat(markerPath); markerErr == nil {
			if !markerInfo.Mode().IsRegular() || markerInfo.Size() > 128 {
				retainedMongoDB = "*"
			} else if marker, readErr := os.ReadFile(markerPath); readErr != nil {
				retainedMongoDB = "*"
			} else {
				retainedMongoDB = strings.TrimSpace(string(marker))
				if !validArchiveName(retainedMongoDB) {
					retainedMongoDB = "*"
				}
			}
		} else if !errors.Is(markerErr, os.ErrNotExist) {
			retainedMongoDB = "*"
		}
	}
	parentSafe := false
	if fd, openErr := openDirectoryNoFollow(root); openErr == nil {
		parentSafe = true
		unix.Close(fd)
	}
	parentInfo, parentErr := os.Lstat(root)
	if parentErr != nil {
		return nil, parentErr
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, errors.New("archive parent metadata is unavailable")
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validArchiveName(entry.Name()) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil, statErr
		}
		candidate := Candidate{Kind: "uninstall_archive", Component: component, Path: path, ModifiedAt: info.ModTime()}
		setIdentity(&candidate, info)
		size, contentHash, safe, walkErr := inspectTree(path)
		if walkErr != nil {
			return nil, walkErr
		}
		candidate.Bytes = size
		candidate.ID = fingerprint(candidate, info, contentHash)
		candidate.Eligible = safe && parentSafe && candidate.device == uint64(parentStat.Dev)
		candidate.ReasonCode = "MANUAL_CONFIRMATION_REQUIRED"
		candidate.Reason = "Archive deletion is irreversible; recovery needs an external backup or reinstall"
		if !candidate.Eligible {
			candidate.ReasonCode = "UNSAFE_ARCHIVE_CONTENT"
			candidate.Reason = "Archive contains a symbolic link, mount, or unsupported file or path"
		}
		if candidate.Eligible && entry.Name() == latest {
			candidate.Eligible = false
			candidate.ReasonCode = "LATEST_RECOVERY_SNAPSHOT"
			candidate.Reason = "Latest archive may still be used to restore component configuration"
		}
		if candidate.Eligible && component == "mongodb" && (retainedMongoDB == "*" || retainedMongoDB == entry.Name()) {
			candidate.Eligible = false
			candidate.ReasonCode = "ACTIVE_RECOVERY_REFERENCE"
			candidate.Reason = "MongoDB retained-data marker still references this archive"
		}
		meta, metaErr := readMetadata(filepath.Join(path, ".oneinstack-artifact.meta"))
		if metaErr == nil && candidate.Eligible && entry.Name() != latest &&
			(component == "mysql" || component == "redis") &&
			meta["component"] == component && meta["schema"] == "1" &&
			artifactpolicy.SupportsArchiveRetention(component, meta["packageVersion"]) &&
			meta["contentType"] == "program-config" &&
			meta["retentionDays"] == "30" && meta["retentionConfirmed"] == "true" &&
			validMetadataPath(meta["installPath"]) && validMetadataPath(meta["dataPath"]) &&
			(meta["logPath"] == "" || validMetadataPath(meta["logPath"])) &&
			!pathInside(meta["dataPath"], meta["installPath"]) &&
			!pathInside(meta["logPath"], meta["installPath"]) {
			createdAt, parseErr := time.Parse(time.RFC3339, meta["createdAt"])
			if parseErr == nil && now.Sub(createdAt) >= 30*24*time.Hour {
				candidate.Automatic = true
				candidate.ReasonCode = "CONFIRMED_RETENTION_EXPIRED"
				candidate.Reason = "Confirmed 30 day retention expired; latest archive is preserved"
			}
		}
		result = append(result, candidate)
	}
	return result, nil
}

func validArchiveName(name string) bool {
	if len(name) != len("20260904T091039Z") || name[8] != 'T' || name[15] != 'Z' {
		return false
	}
	for i, r := range name {
		if i != 8 && i != 15 && (r < '0' || r > '9') {
			return false
		}
	}
	_, err := time.Parse("20060102T150405Z", name)
	return err == nil
}

func pathInside(path, parent string) bool {
	return path != "" && parent != "" && filepath.IsAbs(path) && filepath.IsAbs(parent) &&
		(path == parent || strings.HasPrefix(path, parent+string(filepath.Separator)))
}

func validMetadataPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/"
}

func componentStateRoot() string {
	for _, key := range []string{"ONEINSTACK_COMPONENT_STATE", "COMPONENT_STATE_DIR"} {
		path := strings.TrimSpace(os.Getenv(key))
		if validMetadataPath(path) {
			return path
		}
	}
	return stateRoot
}

func readMetadata(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, errors.New("invalid archive metadata")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(data, []byte{0})
	if len(parts) < 3 || len(parts)%2 != 1 || len(parts[len(parts)-1]) != 0 {
		return nil, errors.New("invalid archive metadata format")
	}
	meta := make(map[string]string)
	for i := 0; i < len(parts)-1; i += 2 {
		meta[string(parts[i])] = string(parts[i+1])
	}
	return meta, nil
}

func inspectTree(root string) (int64, string, bool, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return 0, "", false, err
	}
	rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
	if !ok || !rootInfo.IsDir() {
		return 0, "", false, errors.New("archive root is not a directory")
	}
	hash := sha256.New()
	var size int64
	safe := true
	count := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		count++
		if count > 100000 {
			return errors.New("archive entry count exceeds safe limit")
		}
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			return infoErr
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New("archive file metadata is unavailable")
		}
		if stat.Dev != rootStat.Dev || !(info.IsDir() || info.Mode().IsRegular()) {
			safe = false
		}
		if info.Mode().IsRegular() {
			size += allocatedBytes(info)
		}
		_, _ = fmt.Fprintf(hash, "%s|%d|%d|%d|%d\n", strings.TrimPrefix(path, root), stat.Ino, info.Size(), info.ModTime().UnixNano(), info.Mode())
		return nil
	})
	if err != nil {
		return 0, "", false, err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), safe, nil
}

func fingerprint(candidate Candidate, info os.FileInfo, contentHash string) string {
	stat, _ := info.Sys().(*syscall.Stat_t)
	var device, inode uint64
	if stat != nil {
		device, inode = uint64(stat.Dev), stat.Ino
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		candidate.Kind, candidate.Component, candidate.Path,
		strconv.FormatInt(candidate.Bytes, 10), strconv.FormatInt(info.ModTime().UnixNano(), 10),
		strconv.FormatUint(device, 10), strconv.FormatUint(inode, 10), contentHash,
	}, "|")))
	return hex.EncodeToString(sum[:])
}

func setIdentity(candidate *Candidate, info os.FileInfo) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	candidate.device = uint64(stat.Dev)
	candidate.inode = stat.Ino
	candidate.modNano = info.ModTime().UnixNano()
	candidate.fileSize = info.Size()
}

func allocatedBytes(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks >= 0 {
		return stat.Blocks * 512
	}
	return info.Size()
}
