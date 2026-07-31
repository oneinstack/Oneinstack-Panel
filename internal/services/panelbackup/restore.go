package panelbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const restoreJournalSchema = 1

type restoreEntry struct {
	Target    string `json:"target"`
	Candidate string `json:"candidate,omitempty"`
	Previous  string `json:"previous"`
	Kind      string `json:"kind"`
	HadOld    bool   `json:"hadOld"`
	Installed bool   `json:"installed"`
}

type restoreJournal struct {
	SchemaVersion int            `json:"schemaVersion"`
	BackupID      string         `json:"backupId"`
	CreatedAt     time.Time      `json:"createdAt"`
	Entries       []restoreEntry `json:"entries"`
}

type RestoreTransaction struct {
	mu      sync.Mutex
	manager *Manager
	journal restoreJournal
	closed  bool
}

func (m *Manager) RestoreOffline(ctx context.Context, id, passphrase string) (*RestoreTransaction, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.restoreJournalPath()); err == nil {
		return nil, ErrRecoveryNeeded
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	info, err := m.Get(id)
	if err != nil {
		return nil, err
	}
	prepared, err := m.prepare(ctx, info, passphrase)
	if err != nil {
		return nil, err
	}
	defer prepared.Cleanup()
	operationID, err := randomBackupID()
	if err != nil {
		return nil, err
	}
	journal := restoreJournal{
		SchemaVersion: restoreJournalSchema,
		BackupID:      id, CreatedAt: m.now().UTC(),
	}
	addFile := func(source, target, kind string, mode fs.FileMode) error {
		entry, err := prepareFileEntry(source, target, operationID, kind, mode)
		if err != nil {
			return err
		}
		journal.Entries = append(journal.Entries, entry)
		return nil
	}
	if err := addFile(
		filepath.Join(prepared.Root, "config", "config.yaml"),
		m.config.ConfigPath,
		"config",
		0600,
	); err != nil {
		return nil, err
	}
	if err := addFile(
		filepath.Join(prepared.Root, "database", "myadmin.db"),
		m.config.DatabasePath,
		"database",
		0600,
	); err != nil {
		cleanupJournalCandidates(journal)
		return nil, err
	}
	for _, optional := range []struct {
		source string
		target string
		kind   string
	}{
		{
			source: filepath.Join(prepared.Root, "identity", "panel-instance-id"),
			target: filepath.Join(m.config.BasePath, "panel-instance-id"),
			kind:   "identity",
		},
		{
			source: filepath.Join(prepared.Root, "update", "trusted-keys.json"),
			target: filepath.Join(m.config.BasePath, "update", "trusted-keys.json"),
			kind:   "trust",
		},
	} {
		if _, err := os.Stat(optional.source); err == nil {
			if err := addFile(optional.source, optional.target, optional.kind, 0600); err != nil {
				cleanupJournalCandidates(journal)
				return nil, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupJournalCandidates(journal)
			return nil, err
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		target := m.config.DatabasePath + suffix
		if _, err := os.Lstat(target); err == nil {
			journal.Entries = append(journal.Entries, restoreEntry{
				Target: target, Previous: target + ".one-restore-old-" + operationID,
				Kind: "sqlite-sidecar", HadOld: true,
			})
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupJournalCandidates(journal)
			return nil, err
		}
	}
	if prepared.Manifest.IncludesCertificates {
		certificateSource := filepath.Join(prepared.Root, "certificates")
		if err := os.MkdirAll(certificateSource, 0700); err != nil {
			cleanupJournalCandidates(journal)
			return nil, err
		}
		entry, err := prepareDirectoryEntry(
			certificateSource,
			m.config.CertificatePath,
			operationID,
			"certificates",
		)
		if err != nil {
			cleanupJournalCandidates(journal)
			return nil, err
		}
		journal.Entries = append(journal.Entries, entry)
	}
	if err := m.writeRestoreJournal(journal); err != nil {
		cleanupJournalCandidates(journal)
		return nil, err
	}
	for index := range journal.Entries {
		if err := ctx.Err(); err != nil {
			rollbackErr := rollbackJournal(journal)
			_ = os.Remove(m.restoreJournalPath())
			return nil, errors.Join(err, rollbackErr)
		}
		if err := installRestoreEntry(&journal.Entries[index]); err != nil {
			rollbackErr := rollbackJournal(journal)
			_ = os.Remove(m.restoreJournalPath())
			return nil, errors.Join(err, rollbackErr)
		}
		if err := m.writeRestoreJournal(journal); err != nil {
			rollbackErr := rollbackJournal(journal)
			_ = os.Remove(m.restoreJournalPath())
			return nil, errors.Join(err, rollbackErr)
		}
	}
	return &RestoreTransaction{manager: m, journal: journal}, nil
}

func (transaction *RestoreTransaction) Commit() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return nil
	}
	if err := os.Remove(transaction.manager.restoreJournalPath()); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := syncDirectory(transaction.manager.config.BackupRoot); err != nil {
		return err
	}
	// The missing journal is the durable commit marker. Cleanup after this
	// point is best-effort: a stale old snapshot consumes disk space but must
	// never make the next boot roll back an already healthy restore.
	for _, entry := range transaction.journal.Entries {
		if entry.HadOld {
			_ = removeRestorePath(entry.Previous, entry.Kind)
		}
		if entry.Candidate != "" {
			_ = removeRestorePath(entry.Candidate, entry.Kind)
		}
	}
	transaction.closed = true
	return nil
}

func (transaction *RestoreTransaction) Rollback() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return nil
	}
	err := rollbackJournal(transaction.journal)
	if err == nil {
		_ = os.Remove(transaction.manager.restoreJournalPath())
	}
	transaction.closed = true
	return err
}

func (m *Manager) RecoverInterruptedRestore() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	journal, exists, err := m.readRestoreJournal()
	if err != nil || !exists {
		return err
	}
	if err := rollbackJournal(journal); err != nil {
		return err
	}
	if err := os.Remove(m.restoreJournalPath()); err != nil {
		return err
	}
	return syncDirectory(m.config.BackupRoot)
}

func (m *Manager) NeedsRecovery() (bool, error) {
	info, err := os.Lstat(m.restoreJournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, ErrInvalidBackup
	}
	return true, nil
}

func prepareFileEntry(source, target, operationID, kind string, mode fs.FileMode) (restoreEntry, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) == string(filepath.Separator) {
		return restoreEntry{}, ErrInvalidBackup
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return restoreEntry{}, err
	}
	candidate := target + ".one-restore-new-" + operationID
	previous := target + ".one-restore-old-" + operationID
	if _, err := os.Lstat(candidate); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return restoreEntry{}, fmt.Errorf("restore candidate already exists")
		}
		return restoreEntry{}, err
	}
	if err := copyRegularFile(source, candidate, mode); err != nil {
		return restoreEntry{}, err
	}
	hadOld := false
	if info, err := os.Lstat(target); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			_ = os.Remove(candidate)
			return restoreEntry{}, fmt.Errorf("restore target is not a regular file: %s", target)
		}
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(candidate)
		return restoreEntry{}, err
	}
	return restoreEntry{
		Target: target, Candidate: candidate, Previous: previous,
		Kind: kind, HadOld: hadOld,
	}, nil
}

func prepareDirectoryEntry(source, target, operationID, kind string) (restoreEntry, error) {
	if !filepath.IsAbs(target) || filepath.Clean(target) == string(filepath.Separator) {
		return restoreEntry{}, ErrInvalidBackup
	}
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return restoreEntry{}, err
	}
	candidate := target + ".one-restore-new-" + operationID
	previous := target + ".one-restore-old-" + operationID
	if err := copyDirectory(source, candidate); err != nil {
		return restoreEntry{}, err
	}
	hadOld := false
	if info, err := os.Lstat(target); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			_ = os.RemoveAll(candidate)
			return restoreEntry{}, fmt.Errorf("restore target is not a real directory: %s", target)
		}
		hadOld = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.RemoveAll(candidate)
		return restoreEntry{}, err
	}
	return restoreEntry{
		Target: target, Candidate: candidate, Previous: previous,
		Kind: kind, HadOld: hadOld,
	}, nil
}

func installRestoreEntry(entry *restoreEntry) error {
	if entry.HadOld {
		if err := os.Rename(entry.Target, entry.Previous); err != nil {
			return err
		}
	}
	if entry.Candidate != "" {
		if err := os.Rename(entry.Candidate, entry.Target); err != nil {
			if entry.HadOld {
				_ = os.Rename(entry.Previous, entry.Target)
			}
			return err
		}
	} else if !entry.HadOld {
		return ErrInvalidBackup
	}
	if err := syncDirectory(filepath.Dir(entry.Target)); err != nil {
		return err
	}
	entry.Installed = true
	return nil
}

func rollbackJournal(journal restoreJournal) error {
	var errs []error
	for index := len(journal.Entries) - 1; index >= 0; index-- {
		entry := journal.Entries[index]
		_, previousErr := os.Lstat(entry.Previous)
		previousExists := previousErr == nil
		if previousErr != nil && !errors.Is(previousErr, os.ErrNotExist) {
			errs = append(errs, previousErr)
			continue
		}
		_, candidateErr := os.Lstat(entry.Candidate)
		candidateExists := entry.Candidate != "" && candidateErr == nil
		if candidateErr != nil && entry.Candidate != "" && !errors.Is(candidateErr, os.ErrNotExist) {
			errs = append(errs, candidateErr)
			continue
		}
		// A process can stop between the two renames and the journal update.
		// The existence of the previous/candidate paths is therefore the
		// recovery source of truth, not only the Installed flag.
		if entry.HadOld && previousExists {
			if err := removeRestorePath(entry.Target, entry.Kind); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
				continue
			}
			if err := os.Rename(entry.Previous, entry.Target); err != nil {
				errs = append(errs, err)
				continue
			}
			if err := syncDirectory(filepath.Dir(entry.Target)); err != nil {
				errs = append(errs, err)
				continue
			}
		} else if !entry.HadOld && entry.Candidate != "" && !candidateExists {
			if err := removeRestorePath(entry.Target, entry.Kind); err != nil &&
				!errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		if entry.Candidate != "" {
			_ = removeRestorePath(entry.Candidate, entry.Kind)
		}
	}
	return errors.Join(errs...)
}

func cleanupJournalCandidates(journal restoreJournal) {
	for _, entry := range journal.Entries {
		if entry.Candidate != "" {
			_ = removeRestorePath(entry.Candidate, entry.Kind)
		}
	}
}

func removeRestorePath(path, kind string) error {
	if path == "" {
		return nil
	}
	if kind == "certificates" {
		return os.RemoveAll(path)
	}
	return os.Remove(path)
}

func copyRegularFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrInvalidBackup
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		output.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(destination))
}

func copyDirectory(source, destination string) error {
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	remove := true
	defer func() {
		if remove {
			_ = os.RemoveAll(destination)
		}
	}()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return ErrInvalidBackup
		}
		relative, err := filepath.Rel(source, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrInvalidBackup
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		if !entry.Type().IsRegular() {
			return ErrInvalidBackup
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		return copyRegularFile(path, target, info.Mode().Perm())
	})
	if err != nil {
		return err
	}
	remove = false
	return syncDirectory(filepath.Dir(destination))
}

func (m *Manager) restoreJournalPath() string {
	return filepath.Join(m.config.BackupRoot, "restore-journal.json")
}

func (m *Manager) writeRestoreJournal(journal restoreJournal) error {
	content, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(m.restoreJournalPath(), append(content, '\n'), 0600)
}

func (m *Manager) readRestoreJournal() (restoreJournal, bool, error) {
	content, err := os.ReadFile(m.restoreJournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return restoreJournal{}, false, nil
	}
	if err != nil {
		return restoreJournal{}, false, err
	}
	var journal restoreJournal
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil || ensureJSONEOF(decoder) != nil ||
		journal.SchemaVersion != restoreJournalSchema ||
		!backupIDPattern.MatchString(journal.BackupID) ||
		journal.CreatedAt.IsZero() || len(journal.Entries) < 2 || len(journal.Entries) > 100010 {
		return restoreJournal{}, false, ErrInvalidBackup
	}
	for _, entry := range journal.Entries {
		if !m.validRestoreEntry(entry) {
			return restoreJournal{}, false, ErrInvalidBackup
		}
	}
	return journal, true, nil
}

func (m *Manager) validRestoreEntry(entry restoreEntry) bool {
	allowed := map[string]string{
		m.config.ConfigPath:                                             "config",
		m.config.DatabasePath:                                           "database",
		m.config.DatabasePath + "-wal":                                  "sqlite-sidecar",
		m.config.DatabasePath + "-shm":                                  "sqlite-sidecar",
		filepath.Join(m.config.BasePath, "panel-instance-id"):           "identity",
		filepath.Join(m.config.BasePath, "update", "trusted-keys.json"): "trust",
	}
	if m.config.CertificatePath != "." {
		allowed[m.config.CertificatePath] = "certificates"
	}
	if allowed[filepath.Clean(entry.Target)] != entry.Kind ||
		!validRestoreSibling(entry.Target, entry.Previous, ".one-restore-old-") {
		return false
	}
	if entry.Candidate == "" {
		return entry.Kind == "sqlite-sidecar"
	}
	return validRestoreSibling(entry.Target, entry.Candidate, ".one-restore-new-")
}

func validRestoreSibling(target, sibling, marker string) bool {
	prefix := filepath.Clean(target) + marker
	if !strings.HasPrefix(filepath.Clean(sibling), prefix) {
		return false
	}
	suffix := strings.TrimPrefix(filepath.Clean(sibling), prefix)
	return backupIDPattern.MatchString(suffix)
}

func (m *Manager) WriteRestoreRequest(request RestoreRequest) error {
	if !backupIDPattern.MatchString(strings.TrimSpace(request.BackupID)) {
		return ErrNotFound
	}
	if err := validatePassphrase(request.Passphrase); err != nil {
		return err
	}
	content, err := json.Marshal(request)
	if err != nil {
		return err
	}
	path := m.restoreRequestPath()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ErrRestoreBusy
	}
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(append(content, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func (m *Manager) ConsumeRestoreRequest() (RestoreRequest, error) {
	path := m.restoreRequestPath()
	info, err := os.Lstat(path)
	if err != nil {
		return RestoreRequest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return RestoreRequest{}, ErrInvalidBackup
	}
	content, err := os.ReadFile(path)
	_ = os.Remove(path)
	if err != nil {
		return RestoreRequest{}, err
	}
	var request RestoreRequest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || ensureJSONEOF(decoder) != nil ||
		!backupIDPattern.MatchString(request.BackupID) ||
		validatePassphrase(request.Passphrase) != nil {
		return RestoreRequest{}, ErrInvalidBackup
	}
	return request, nil
}

func (m *Manager) RemoveRestoreRequest() {
	_ = os.Remove(m.restoreRequestPath())
	_ = syncDirectory(m.config.BackupRoot)
}

func (m *Manager) restoreRequestPath() string {
	return filepath.Join(m.config.BackupRoot, "restore-request.json")
}

func (m *Manager) WriteStatus(status RestoreStatus) error {
	status.UpdatedAt = status.UpdatedAt.UTC()
	content, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(m.config.BackupRoot, "restore-status.json"), append(content, '\n'), 0600)
}

func (m *Manager) Status() (RestoreStatus, error) {
	path := filepath.Join(m.config.BackupRoot, "restore-status.json")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return RestoreStatus{State: StatusIdle, UpdatedAt: m.now().UTC()}, nil
	}
	if err != nil {
		return RestoreStatus{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 64<<10 {
		return RestoreStatus{}, ErrInvalidBackup
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return RestoreStatus{}, err
	}
	var status RestoreStatus
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil || ensureJSONEOF(decoder) != nil ||
		!validRestoreStatus(status) {
		return RestoreStatus{}, ErrInvalidBackup
	}
	return status, nil
}

func validRestoreStatus(status RestoreStatus) bool {
	switch status.State {
	case StatusIdle, StatusValidating, StatusStopping, StatusRestoring,
		StatusHealthCheck, StatusSucceeded, StatusFailed, StatusRolledBack,
		StatusRollbackError:
	default:
		return false
	}
	if status.UpdatedAt.IsZero() {
		return false
	}
	return status.BackupID == "" || backupIDPattern.MatchString(status.BackupID)
}
