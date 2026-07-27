package filemanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	trashMetadataVersion = 1
	trashFilesDirectory  = internalDirectoryName + "/files"
	trashMetadataDir     = internalDirectoryName + "/metadata"
	maxTrashMetadataSize = 64 << 10
	maxTrashEntries      = 10000
)

var trashMu sync.Mutex

type TrashEntry struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	OriginalPath string    `json:"originalPath"`
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	IsDir        bool      `json:"isDir"`
	DeletedAt    time.Time `json:"deletedAt"`
	DeletedBy    string    `json:"deletedBy,omitempty"`
}

func (m *Manager) MoveToTrash(virtualPath, deletedBy string) (TrashEntry, error) {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return TrashEntry{}, err
	}
	relative, err := m.Relative(virtualPath)
	if err != nil {
		return TrashEntry{}, err
	}
	if relative == "." {
		return TrashEntry{}, ErrRootOperation
	}
	info, err := m.root.Lstat(relative)
	if err != nil {
		return TrashEntry{}, err
	}

	entry := TrashEntry{
		Version:      trashMetadataVersion,
		ID:           uuid.NewString(),
		OriginalPath: m.VirtualPath(relative),
		Name:         pathpkg.Base(relative),
		Size:         info.Size(),
		IsDir:        info.IsDir(),
		DeletedAt:    time.Now().UTC(),
		DeletedBy:    strings.TrimSpace(deletedBy),
	}
	if err := m.writeTrashMetadata(entry); err != nil {
		return TrashEntry{}, err
	}

	target := pathpkg.Join(trashFilesDirectory, entry.ID)
	if err := m.renameRelativeExclusive(relative, target); err != nil {
		_ = m.root.Remove(trashMetadataPath(entry.ID))
		return TrashEntry{}, err
	}
	return entry, nil
}

func (m *Manager) ListTrash() ([]TrashEntry, error) {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return nil, err
	}
	return m.listTrashLocked()
}

func (m *Manager) RestoreTrash(id string) (TrashEntry, error) {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return TrashEntry{}, err
	}
	entry, err := m.readTrashMetadata(id)
	if err != nil {
		return TrashEntry{}, err
	}
	relative, err := m.Relative(entry.OriginalPath)
	if err != nil {
		return TrashEntry{}, fmt.Errorf("invalid trash metadata: %w", err)
	}
	if relative == "." {
		return TrashEntry{}, ErrRootOperation
	}

	if _, err := m.root.Lstat(relative); err == nil {
		return TrashEntry{}, fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return TrashEntry{}, err
	}
	parent := pathpkg.Dir(relative)
	if parent != "." {
		if err := m.mkdirAllRelative(parent, 0755); err != nil {
			return TrashEntry{}, err
		}
	}

	source := pathpkg.Join(trashFilesDirectory, entry.ID)
	if err := m.renameRelativeExclusive(source, relative); err != nil {
		return TrashEntry{}, err
	}
	_ = m.root.Remove(trashMetadataPath(entry.ID))
	return entry, nil
}

func (m *Manager) DeleteTrashPermanently(id string) error {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return err
	}
	return m.deleteTrashLocked(id)
}

func (m *Manager) EmptyTrash() (int, error) {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return 0, err
	}
	files, err := m.readInternalDirectory(trashFilesDirectory)
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, entry := range files {
		if err := ValidateName(entry.Name()); err != nil {
			continue
		}
		if err := m.removeAllRelative(pathpkg.Join(trashFilesDirectory, entry.Name())); err != nil {
			return deleted, err
		}
		deleted++
	}

	metadata, err := m.readInternalDirectory(trashMetadataDir)
	if err != nil {
		return deleted, err
	}
	for _, entry := range metadata {
		if err := ValidateName(entry.Name()); err != nil {
			continue
		}
		if err := m.root.Remove(pathpkg.Join(trashMetadataDir, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return deleted, err
		}
	}
	return deleted, nil
}

func (m *Manager) CleanupTrashBefore(cutoff time.Time) (int, error) {
	trashMu.Lock()
	defer trashMu.Unlock()

	if err := m.ensureTrashDirectories(); err != nil {
		return 0, err
	}
	entries, err := m.listTrashLocked()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, entry := range entries {
		if entry.DeletedAt.Before(cutoff) {
			if err := m.deleteTrashLocked(entry.ID); err != nil {
				return deleted, err
			}
			deleted++
		}
	}
	return deleted, nil
}

func (m *Manager) listTrashLocked() ([]TrashEntry, error) {
	metadata, err := m.readInternalDirectory(trashMetadataDir)
	if err != nil {
		return nil, err
	}
	entries := make([]TrashEntry, 0, min(len(metadata), maxTrashEntries))
	for _, metadataFile := range metadata {
		if len(entries) >= maxTrashEntries {
			break
		}
		if metadataFile.IsDir() || !strings.HasSuffix(metadataFile.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(metadataFile.Name(), ".json")
		entry, err := m.readTrashMetadata(id)
		if err != nil {
			continue
		}
		info, err := m.root.Lstat(pathpkg.Join(trashFilesDirectory, id))
		if err != nil {
			continue
		}
		entry.Size = info.Size()
		entry.IsDir = info.IsDir()
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].DeletedAt.After(entries[j].DeletedAt)
	})
	return entries, nil
}

func (m *Manager) deleteTrashLocked(id string) error {
	entry, err := m.readTrashMetadata(id)
	if err != nil {
		return err
	}
	if err := m.removeAllRelative(pathpkg.Join(trashFilesDirectory, entry.ID)); err != nil {
		return err
	}
	if err := m.root.Remove(trashMetadataPath(entry.ID)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (m *Manager) ensureTrashDirectories() error {
	for _, directory := range []string{internalDirectoryName, trashFilesDirectory, trashMetadataDir} {
		if err := m.root.Mkdir(directory, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := m.root.Lstat(directory)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: trash directory is not a safe directory", ErrInvalidPath)
		}
		file, err := m.root.Open(directory)
		if err != nil {
			return err
		}
		chmodErr := file.Chmod(0700)
		closeErr := file.Close()
		if chmodErr != nil {
			return chmodErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *Manager) writeTrashMetadata(entry TrashEntry) (err error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	metadataPath := trashMetadataPath(entry.ID)
	file, err := m.root.OpenFile(metadataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	keepFile := false
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
		if err != nil || !keepFile {
			_ = m.root.Remove(metadataPath)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	keepFile = true
	return nil
}

func (m *Manager) readTrashMetadata(id string) (TrashEntry, error) {
	if err := validateTrashID(id); err != nil {
		return TrashEntry{}, err
	}
	file, err := m.root.Open(trashMetadataPath(id))
	if err != nil {
		return TrashEntry{}, err
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, maxTrashMetadataSize))
	decoder.DisallowUnknownFields()
	var entry TrashEntry
	if err := decoder.Decode(&entry); err != nil {
		return TrashEntry{}, fmt.Errorf("decode trash metadata: %w", err)
	}
	if entry.Version != trashMetadataVersion || entry.ID != id || entry.DeletedAt.IsZero() {
		return TrashEntry{}, errors.New("invalid trash metadata")
	}
	relative, err := m.Relative(entry.OriginalPath)
	if err != nil || relative == "." || entry.Name != pathpkg.Base(relative) {
		return TrashEntry{}, errors.New("invalid trash metadata path")
	}
	return entry, nil
}

func (m *Manager) readInternalDirectory(relative string) ([]os.DirEntry, error) {
	file, err := m.root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.ReadDir(-1)
}

func (m *Manager) renameRelativeExclusive(source, target string) error {
	if !fs.ValidPath(source) || !fs.ValidPath(target) || source == "." || target == "." {
		return ErrInvalidPath
	}
	sourceParentPath := pathpkg.Dir(source)
	targetParentPath := pathpkg.Dir(target)
	sourceParent, err := m.root.Open(sourceParentPath)
	if err != nil {
		return err
	}
	defer sourceParent.Close()
	targetParent, err := m.root.Open(targetParentPath)
	if err != nil {
		return err
	}
	defer targetParent.Close()

	sourceInfo, err := sourceParent.Stat()
	if err != nil {
		return err
	}
	targetInfo, err := targetParent.Stat()
	if err != nil {
		return err
	}
	if !sourceInfo.IsDir() || !targetInfo.IsDir() {
		return ErrInvalidPath
	}
	return renameExclusive(sourceParent, pathpkg.Base(source), targetParent, pathpkg.Base(target))
}

func (m *Manager) mkdirAllRelative(relative string, perm os.FileMode) error {
	if !fs.ValidPath(relative) || relative == "." || isInternalPath(relative) {
		return ErrInvalidPath
	}
	current := ""
	for _, segment := range strings.Split(relative, "/") {
		if current == "" {
			current = segment
		} else {
			current = pathpkg.Join(current, segment)
		}
		if err := m.root.Mkdir(current, perm); err != nil {
			if !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, statErr := m.root.Stat(current)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("%w: %s is not a directory", ErrInvalidPath, current)
			}
		}
	}
	return nil
}

func trashMetadataPath(id string) string {
	return pathpkg.Join(trashMetadataDir, id+".json")
}

func validateTrashID(id string) error {
	parsed, err := uuid.Parse(strings.TrimSpace(id))
	if err != nil || parsed.String() != id {
		return ErrInvalidName
	}
	return nil
}
