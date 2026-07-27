package panelupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type Manager struct {
	config  Config
	client  *http.Client
	runner  CommandRunner
	service ServiceController
	health  HealthChecker
	now     func() time.Time
}

type updateSnapshot struct {
	OperationID       string            `json:"operationId"`
	BackupPath        string            `json:"backupPath"`
	PreviousTarget    string            `json:"previousTarget"`
	OldBundledPath    string            `json:"oldBundledPath,omitempty"`
	BundledSwitchPlan bool              `json:"bundledSwitchPlanned"`
	BundledHadOld     bool              `json:"bundledHadPrevious"`
	BundledSwitched   bool              `json:"bundledSwitched"`
	TargetReleasePath string            `json:"targetReleasePath,omitempty"`
	WasActive         bool              `json:"wasActive"`
	Files             map[string]string `json:"files"`
	ManagedFiles      []string          `json:"managedFiles"`
	CreatedAt         time.Time         `json:"createdAt"`
}

func NewManager(config Config) (*Manager, error) {
	if config.OS == "" {
		config.OS = runtime.GOOS
	}
	if config.Arch == "" {
		config.Arch = runtime.GOARCH
	}
	config.InstallDir = filepath.Clean(strings.TrimSpace(config.InstallDir))
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	runner := OSCommandRunner{}
	return &Manager{
		config:  config,
		client:  secureHTTPClient(config.RequestTimeout),
		runner:  runner,
		service: SystemdController{Runner: runner, Unit: "one.service"},
		health:  HTTPHealthChecker{},
		now:     time.Now,
	}, nil
}

func validateConfig(config Config) error {
	if !filepath.IsAbs(config.InstallDir) || config.InstallDir == string(filepath.Separator) {
		return fmt.Errorf("update install directory must be a non-root absolute path")
	}
	if config.Channel != "stable" && config.Channel != "beta" && config.Channel != "development" {
		return fmt.Errorf("invalid update channel")
	}
	if config.MaxPackageBytes < 1<<20 || config.MaxExpandedBytes < config.MaxPackageBytes {
		return fmt.Errorf("invalid update size limits")
	}
	if config.BackupRetention < 1 {
		return fmt.Errorf("backup retention must be positive")
	}
	if config.Enabled && strings.TrimSpace(config.ManifestURL) == "" && strings.TrimSpace(config.ResolveURL) == "" {
		return fmt.Errorf("update manifest URL or Center resolve URL is required")
	}
	if strings.TrimSpace(config.ResolveURL) != "" && !instanceIDPattern.MatchString(config.InstanceID) {
		return fmt.Errorf("valid panel instance ID is required for Center-managed updates")
	}
	return nil
}

func (m *Manager) Check(ctx context.Context) (CheckResult, error) {
	result, _, _, err := CheckUpdate(ctx, m.client, m.config)
	return result, err
}

func (m *Manager) Apply(ctx context.Context) (finalStatus Status, finalErr error) {
	lock, err := m.acquireLock()
	if err != nil {
		return Status{}, err
	}
	defer releaseLock(lock)
	if _, err := os.Stat(m.journalPath()); err == nil {
		return Status{}, ErrRecoveryNeeded
	} else if !errors.Is(err, os.ErrNotExist) {
		return Status{}, fmt.Errorf("inspect active update transaction: %w", err)
	}

	started := m.now().UTC()
	status := Status{
		State: StateChecking, CurrentVersion: m.config.CurrentVersion,
		StartedAt: &started, UpdatedAt: started,
	}
	if err := m.writeStatus(status); err != nil {
		return status, err
	}
	defer func() {
		finalStatus = status
	}()

	result, manifest, artifact, err := CheckUpdate(ctx, m.client, m.config)
	if err != nil {
		return m.fail(&status, err)
	}
	status.TargetVersion = manifest.Version
	if !result.UpdateAvailable {
		return m.fail(&status, ErrNoUpdate)
	}
	if err := m.transition(&status, StateDownloading, "正在下载并校验签名发布包"); err != nil {
		return status, err
	}

	operationID := m.now().UTC().Format("20060102T150405.000000000Z")
	stagingRoot := filepath.Join(m.updateRoot(), "staging", operationID)
	if err := os.MkdirAll(stagingRoot, 0700); err != nil {
		return m.fail(&status, err)
	}
	defer os.RemoveAll(stagingRoot)
	archivePath := filepath.Join(stagingRoot, artifact.FileName)
	if err := m.downloadArtifact(ctx, artifact, archivePath); err != nil {
		return m.fail(&status, err)
	}
	payloadPath := filepath.Join(stagingRoot, "payload")
	if err := os.MkdirAll(payloadPath, 0700); err != nil {
		return m.fail(&status, err)
	}
	if _, err := extractRelease(archivePath, payloadPath, m.config.MaxExpandedBytes); err != nil {
		return m.fail(&status, err)
	}
	candidateBinary := filepath.Join(payloadPath, "one")
	if err := m.verifyCandidate(ctx, candidateBinary, manifest.Version); err != nil {
		return m.fail(&status, err)
	}

	wasActive := m.service.IsActive(ctx)
	snapshot, err := m.beginSnapshot(operationID, wasActive)
	if err != nil {
		return m.fail(&status, fmt.Errorf("begin update snapshot: %w", err))
	}
	status.BackupPath = snapshot.BackupPath

	rollbackOnFailure := func(cause error) (Status, error) {
		status.RollbackAttempted = true
		rollbackErr := m.rollback(ctx, snapshot)
		finished := m.now().UTC()
		status.FinishedAt = &finished
		if rollbackErr != nil {
			status.State = StateRollbackFailed
			status.Message = fmt.Sprintf("更新失败且自动回滚失败：%v；回滚错误：%v", cause, rollbackErr)
			_ = m.writeStatus(status)
			return status, fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
		}
		status.State = StateRolledBack
		status.RollbackSucceeded = true
		status.Message = "更新失败，已自动恢复旧版本：" + cause.Error()
		_ = m.writeStatus(status)
		_ = os.Remove(m.journalPath())
		return status, cause
	}

	if wasActive {
		if err := m.service.Stop(ctx); err != nil {
			return rollbackOnFailure(fmt.Errorf("stop panel service: %w", err))
		}
	}
	populatedSnapshot, err := m.populateSnapshot(snapshot)
	if err != nil {
		return rollbackOnFailure(fmt.Errorf("create update snapshot: %w", err))
	}
	snapshot = populatedSnapshot

	if err := m.transition(&status, StatePreflight, "正在使用数据库副本执行迁移预检"); err != nil {
		return rollbackOnFailure(err)
	}
	if err := m.preflight(ctx, candidateBinary, snapshot); err != nil {
		return rollbackOnFailure(fmt.Errorf("database migration preflight: %w", err))
	}
	if err := m.transition(&status, StateSwitching, "正在原子切换面板版本"); err != nil {
		return rollbackOnFailure(err)
	}
	releasePath, err := m.releasePath(manifest.Version)
	if err != nil {
		return rollbackOnFailure(err)
	}
	snapshot.TargetReleasePath = releasePath
	if err := m.writeJournal(snapshot); err != nil {
		return rollbackOnFailure(err)
	}
	if err := m.promoteRelease(payloadPath, releasePath, operationID); err != nil {
		return rollbackOnFailure(err)
	}
	currentPath := filepath.Join(m.config.InstallDir, "current")
	releasesRoot := filepath.Join(m.config.InstallDir, "releases")
	previousTarget, err := m.ensureReleaseLayout(currentPath, releasesRoot, operationID)
	if err != nil {
		return rollbackOnFailure(err)
	}
	snapshot.PreviousTarget = previousTarget
	if err := m.writeJournal(snapshot); err != nil {
		return rollbackOnFailure(err)
	}
	target := filepath.Join("releases", filepath.Base(releasePath))
	if err := atomicSymlink(target, currentPath, operationID); err != nil {
		return rollbackOnFailure(err)
	}
	oldBundled, bundledHadOld, err := m.planBundledSwitch(operationID)
	if err != nil {
		return rollbackOnFailure(err)
	}
	snapshot.BundledSwitchPlan = true
	snapshot.BundledHadOld = bundledHadOld
	snapshot.OldBundledPath = oldBundled
	if err := m.writeJournal(snapshot); err != nil {
		return rollbackOnFailure(err)
	}
	if err := m.switchBundled(releasePath, operationID, bundledHadOld); err != nil {
		return rollbackOnFailure(err)
	}
	snapshot.BundledSwitched = true
	if err := m.writeJournal(snapshot); err != nil {
		return rollbackOnFailure(err)
	}

	if wasActive {
		if err := m.service.Start(ctx); err != nil {
			return rollbackOnFailure(fmt.Errorf("start updated panel service: %w", err))
		}
		if err := m.transition(&status, StateHealthChecking, "正在等待新版本健康检查"); err != nil {
			return rollbackOnFailure(err)
		}
		if err := m.health.WaitReady(ctx, m.config.HealthURL, m.config.HealthTimeout); err != nil {
			return rollbackOnFailure(err)
		}
	}

	finished := m.now().UTC()
	status.State = StateSucceeded
	status.Message = fmt.Sprintf("面板已从 %s 更新到 %s", m.config.CurrentVersion, manifest.Version)
	status.FinishedAt = &finished
	if err := m.writeStatus(status); err != nil {
		return rollbackOnFailure(fmt.Errorf("persist successful update status: %w", err))
	}
	if err := os.Remove(m.journalPath()); err != nil {
		return rollbackOnFailure(fmt.Errorf("commit update transaction: %w", err))
	}
	if bundledHadOld {
		_ = os.RemoveAll(oldBundled)
	}
	m.cleanupBackups()
	return status, nil
}

func (m *Manager) planBundledSwitch(operationID string) (string, bool, error) {
	parent := filepath.Join(m.config.InstallDir, "script-registry")
	active := filepath.Join(parent, "bundled")
	next := filepath.Join(parent, ".bundled.new-"+operationID)
	old := filepath.Join(parent, ".bundled.old-"+operationID)
	for _, path := range []string{next, old} {
		if _, err := os.Lstat(path); err == nil {
			return "", false, fmt.Errorf("temporary bundled script path already exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	if info, err := os.Lstat(active); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", false, fmt.Errorf("active bundled scripts are not a regular directory")
		}
		return old, true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	return old, false, nil
}

func (m *Manager) switchBundled(releasePath, operationID string, oldExists bool) error {
	source := filepath.Join(releasePath, "script-registry", "bundled")
	parent := filepath.Join(m.config.InstallDir, "script-registry")
	active := filepath.Join(parent, "bundled")
	next := filepath.Join(parent, ".bundled.new-"+operationID)
	old := filepath.Join(parent, ".bundled.old-"+operationID)
	if err := os.MkdirAll(parent, 0750); err != nil {
		return err
	}
	if err := copyTree(source, next); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(next, ".update-operation"), []byte(operationID+"\n"), 0600); err != nil {
		return err
	}
	if oldExists {
		if err := os.Rename(active, old); err != nil {
			return err
		}
	}
	if err := os.Rename(next, active); err != nil {
		if oldExists {
			_ = os.Rename(old, active)
		}
		return err
	}
	return nil
}

func (m *Manager) rollbackBundled(snapshot updateSnapshot) error {
	if !snapshot.BundledSwitchPlan {
		return nil
	}
	parent := filepath.Join(m.config.InstallDir, "script-registry")
	active := filepath.Join(parent, "bundled")
	next := filepath.Join(parent, ".bundled.new-"+snapshot.OperationID)
	var failures []string
	if err := os.RemoveAll(next); err != nil {
		failures = append(failures, "remove pending bundled scripts: "+err.Error())
	}
	if snapshot.BundledHadOld {
		if _, err := os.Lstat(snapshot.OldBundledPath); err == nil {
			if err := os.RemoveAll(active); err != nil {
				failures = append(failures, "remove new bundled scripts: "+err.Error())
			} else if err := os.Rename(snapshot.OldBundledPath, active); err != nil {
				failures = append(failures, "restore bundled scripts: "+err.Error())
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, "inspect bundled backup: "+err.Error())
		} else if snapshot.BundledSwitched {
			info, activeErr := os.Lstat(active)
			marker, markerErr := os.ReadFile(filepath.Join(active, ".update-operation"))
			activeIsCurrent := markerErr == nil && strings.TrimSpace(string(marker)) == snapshot.OperationID
			if activeErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || activeIsCurrent {
				failures = append(failures, "bundled script backup is missing")
			}
		}
	} else {
		marker, err := os.ReadFile(filepath.Join(active, ".update-operation"))
		if err == nil && strings.TrimSpace(string(marker)) == snapshot.OperationID {
			if err := os.RemoveAll(active); err != nil {
				failures = append(failures, "remove new bundled scripts: "+err.Error())
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, "inspect bundled ownership marker: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) RollbackLast(ctx context.Context) (Status, error) {
	lock, err := m.acquireLock()
	if err != nil {
		return Status{}, err
	}
	defer releaseLock(lock)
	snapshot, err := m.readJournal()
	if err != nil {
		return Status{}, err
	}
	status, _ := m.Status()
	status.RollbackAttempted = true
	if err := m.rollback(ctx, snapshot); err != nil {
		status.State = StateRollbackFailed
		status.Message = "手动回滚失败：" + err.Error()
		_ = m.writeStatus(status)
		return status, err
	}
	finished := m.now().UTC()
	status.State = StateRolledBack
	status.RollbackSucceeded = true
	status.FinishedAt = &finished
	status.Message = "已恢复更新前版本"
	if err := m.writeStatus(status); err != nil {
		return status, err
	}
	_ = os.Remove(m.journalPath())
	return status, nil
}

func (m *Manager) fail(status *Status, err error) (Status, error) {
	finished := m.now().UTC()
	status.State = StateFailed
	status.Message = err.Error()
	status.FinishedAt = &finished
	_ = m.writeStatus(*status)
	return *status, err
}

func (m *Manager) downloadArtifact(ctx context.Context, artifact Artifact, destination string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, artifact.URL, nil)
	if err != nil {
		return err
	}
	response, err := m.client.Do(request)
	if err != nil {
		return fmt.Errorf("download release artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download release artifact: HTTP status %d", response.StatusCode)
	}
	if response.ContentLength > artifact.Size || response.ContentLength > m.config.MaxPackageBytes {
		return fmt.Errorf("release artifact Content-Length exceeds signed size")
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(file, hash), io.LimitReader(response.Body, artifact.Size+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != artifact.Size {
		return fmt.Errorf("release artifact size mismatch: got %d, want %d", written, artifact.Size)
	}
	if hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return fmt.Errorf("release artifact SHA-256 mismatch")
	}
	return nil
}

func (m *Manager) verifyCandidate(ctx context.Context, binary, expectedVersion string) error {
	output, err := m.runner.Run(ctx, Command{Name: binary, Args: []string{"version"}})
	if err != nil {
		return fmt.Errorf("execute candidate version check: %w", err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "Version:") {
			version := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "Version:"))
			if canonicalVersion(version) != canonicalVersion(expectedVersion) {
				return fmt.Errorf("candidate reports version %q, expected %q", version, expectedVersion)
			}
			return nil
		}
	}
	return fmt.Errorf("candidate version output is missing Version field")
}

func (m *Manager) beginSnapshot(operationID string, wasActive bool) (updateSnapshot, error) {
	backupPath := filepath.Join(m.updateRoot(), "backups", operationID)
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		return updateSnapshot{}, err
	}
	if err := os.Mkdir(backupPath, 0700); err != nil {
		return updateSnapshot{}, err
	}
	snapshot := updateSnapshot{
		OperationID: operationID, BackupPath: backupPath, WasActive: wasActive,
		Files: make(map[string]string), CreatedAt: m.now().UTC(),
	}
	if err := m.writeJournal(snapshot); err != nil {
		return updateSnapshot{}, err
	}
	return snapshot, nil
}

func (m *Manager) populateSnapshot(snapshot updateSnapshot) (updateSnapshot, error) {
	backupPath := snapshot.BackupPath
	for _, name := range []string{"config.yaml", "myadmin.db", "myadmin.db-wal", "myadmin.db-shm"} {
		live := filepath.Join(m.config.InstallDir, name)
		snapshot.ManagedFiles = append(snapshot.ManagedFiles, live)
		info, err := os.Stat(live)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return snapshot, err
		}
		backup := filepath.Join(backupPath, "files", name)
		if err := copyFile(live, backup, info.Mode().Perm()); err != nil {
			return snapshot, err
		}
		snapshot.Files[live] = backup
	}
	binary := filepath.Join(m.config.InstallDir, "one")
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() {
		backup := filepath.Join(backupPath, "files", "one")
		if err := copyFile(binary, backup, 0750); err != nil {
			return snapshot, err
		}
		snapshot.Files[binary] = backup
	} else if err != nil {
		return snapshot, err
	}
	if err := m.writeSnapshot(filepath.Join(backupPath, "snapshot.json"), snapshot); err != nil {
		return snapshot, err
	}
	if err := m.writeJournal(snapshot); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (m *Manager) createSnapshot(operationID string, wasActive bool) (updateSnapshot, error) {
	snapshot, err := m.beginSnapshot(operationID, wasActive)
	if err != nil {
		return updateSnapshot{}, err
	}
	return m.populateSnapshot(snapshot)
}

func (m *Manager) preflight(ctx context.Context, candidate string, snapshot updateSnapshot) error {
	preflightRoot := filepath.Join(snapshot.BackupPath, "preflight")
	if err := os.MkdirAll(preflightRoot, 0700); err != nil {
		return err
	}
	for _, name := range []string{"config.yaml", "myadmin.db", "myadmin.db-wal", "myadmin.db-shm"} {
		live := filepath.Join(m.config.InstallDir, name)
		backup, ok := snapshot.Files[live]
		if !ok {
			continue
		}
		info, err := os.Stat(backup)
		if err != nil {
			return err
		}
		if err := copyFile(backup, filepath.Join(preflightRoot, name), info.Mode().Perm()); err != nil {
			return err
		}
	}
	_, err := m.runner.Run(ctx, Command{
		Name: candidate, Args: []string{"update", "preflight"},
		Env: map[string]string{
			"ONEINSTACK_BASE_PATH":   preflightRoot,
			"ONEINSTACK_CONFIG_PATH": filepath.Join(preflightRoot, "config.yaml"),
		},
	})
	return err
}

func (m *Manager) releasePath(version string) (string, error) {
	releasesRoot := filepath.Join(m.config.InstallDir, "releases")
	if err := os.MkdirAll(releasesRoot, 0750); err != nil {
		return "", err
	}
	releaseName := strings.TrimPrefix(canonicalVersion(version), "v")
	releasePath := filepath.Join(releasesRoot, releaseName)
	if releaseName == "" || releasePath == releasesRoot || !pathWithin(releasesRoot, releasePath) {
		return "", fmt.Errorf("unsafe release path")
	}
	return releasePath, nil
}

func (m *Manager) promoteRelease(payloadPath, releasePath, operationID string) error {
	if _, err := os.Stat(releasePath); err == nil {
		return fmt.Errorf("release directory already exists: %s", filepath.Base(releasePath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(
		filepath.Join(payloadPath, ".update-operation"),
		[]byte(operationID+"\n"),
		0600,
	); err != nil {
		return fmt.Errorf("write release ownership marker: %w", err)
	}
	if err := os.Rename(payloadPath, releasePath); err != nil {
		return fmt.Errorf("promote release directory: %w", err)
	}
	return nil
}

func (m *Manager) ensureReleaseLayout(currentPath, releasesRoot, operationID string) (string, error) {
	if info, err := os.Lstat(currentPath); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return "", fmt.Errorf("current release pointer is not a symbolic link")
		}
		target, err := os.Readlink(currentPath)
		if err != nil {
			return "", err
		}
		resolved := filepath.Join(m.config.InstallDir, target)
		if filepath.IsAbs(target) || !pathWithin(releasesRoot, resolved) {
			return "", fmt.Errorf("current release pointer escapes install directory")
		}
		if info, err := os.Stat(filepath.Join(resolved, "one")); err != nil || !info.Mode().IsRegular() {
			return "", fmt.Errorf("current release pointer does not contain a regular one binary")
		}
		return target, m.ensureBinarySymlink(operationID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	legacyName := "legacy-" + strings.TrimPrefix(canonicalVersion(m.config.CurrentVersion), "v") + "-" + operationID
	legacyPath := filepath.Join(releasesRoot, legacyName)
	if err := os.MkdirAll(legacyPath, 0750); err != nil {
		return "", err
	}
	binary := filepath.Join(m.config.InstallDir, "one")
	if err := copyFile(binary, filepath.Join(legacyPath, "one"), 0750); err != nil {
		return "", err
	}
	bundled := filepath.Join(m.config.InstallDir, "script-registry", "bundled")
	if info, err := os.Stat(bundled); err == nil && info.IsDir() {
		if err := copyTree(bundled, filepath.Join(legacyPath, "script-registry", "bundled")); err != nil {
			return "", err
		}
	}
	target := filepath.Join("releases", legacyName)
	if err := atomicSymlink(target, currentPath, operationID); err != nil {
		return "", err
	}
	if err := m.ensureBinarySymlink(operationID); err != nil {
		_ = os.Remove(currentPath)
		return "", err
	}
	return target, nil
}

func (m *Manager) ensureBinarySymlink(operationID string) error {
	binary := filepath.Join(m.config.InstallDir, "one")
	if info, err := os.Lstat(binary); err == nil && info.Mode()&os.ModeSymlink != 0 {
		target, readErr := os.Readlink(binary)
		if readErr == nil && target == filepath.Join("current", "one") {
			return nil
		}
	}
	return atomicSymlink(filepath.Join("current", "one"), binary, operationID)
}

func (m *Manager) rollback(ctx context.Context, snapshot updateSnapshot) error {
	var failures []string
	if m.service.IsActive(ctx) {
		if err := m.service.Stop(ctx); err != nil {
			failures = append(failures, "stop service: "+err.Error())
		}
	}
	if snapshot.PreviousTarget != "" {
		if err := atomicSymlink(snapshot.PreviousTarget, filepath.Join(m.config.InstallDir, "current"), "rollback"); err != nil {
			failures = append(failures, "restore release pointer: "+err.Error())
		}
	}
	if err := m.rollbackBundled(snapshot); err != nil {
		failures = append(failures, err.Error())
	}
	if err := m.removeTargetRelease(snapshot.TargetReleasePath, snapshot.OperationID); err != nil {
		failures = append(failures, "remove failed release: "+err.Error())
	}
	for _, live := range snapshot.ManagedFiles {
		if err := os.Remove(live); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, "remove migrated file: "+err.Error())
		}
		if backup, ok := snapshot.Files[live]; ok {
			info, err := os.Stat(backup)
			if err != nil {
				failures = append(failures, "stat backup: "+err.Error())
				continue
			}
			if err := copyFile(backup, live, info.Mode().Perm()); err != nil {
				failures = append(failures, "restore file: "+err.Error())
			}
		}
	}
	if snapshot.WasActive {
		if err := m.service.Start(ctx); err != nil {
			failures = append(failures, "start old service: "+err.Error())
		} else if err := m.health.WaitReady(ctx, m.config.HealthURL, m.config.HealthTimeout); err != nil {
			failures = append(failures, "old service health check: "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func (m *Manager) removeTargetRelease(target, operationID string) error {
	if target == "" {
		return nil
	}
	releasesRoot := filepath.Join(m.config.InstallDir, "releases")
	target = filepath.Clean(target)
	if target == filepath.Clean(releasesRoot) || !pathWithin(releasesRoot, target) {
		return fmt.Errorf("target release path escapes releases directory")
	}
	currentPath := filepath.Join(m.config.InstallDir, "current")
	if currentTarget, err := os.Readlink(currentPath); err == nil {
		resolved := filepath.Clean(filepath.Join(m.config.InstallDir, currentTarget))
		if resolved == target {
			return fmt.Errorf("refusing to remove the active release")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect current release pointer: %w", err)
	}
	marker, err := os.ReadFile(filepath.Join(target, ".update-operation"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect release ownership marker: %w", err)
	}
	if strings.TrimSpace(string(marker)) != operationID {
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return nil
}

func (m *Manager) updateRoot() string {
	return filepath.Join(m.config.InstallDir, "updates")
}

func (m *Manager) statusPath() string {
	return filepath.Join(m.updateRoot(), "status.json")
}

func (m *Manager) journalPath() string {
	return filepath.Join(m.updateRoot(), "active-transaction.json")
}

func (m *Manager) lockPath() string {
	return filepath.Join(m.updateRoot(), "update.lock")
}

func (m *Manager) acquireLock() (*os.File, error) {
	if err := os.MkdirAll(m.updateRoot(), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(m.lockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, ErrUpdateBusy
	}
	return file, nil
}

func releaseLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func atomicSymlink(target, path, suffix string) error {
	temp := path + ".new-" + suffix
	_ = os.Remove(temp)
	if err := os.Symlink(target, temp); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

func (m *Manager) writeJournal(snapshot updateSnapshot) error {
	if err := m.writeSnapshot(m.journalPath(), snapshot); err != nil {
		return err
	}
	return m.writeSnapshot(filepath.Join(snapshot.BackupPath, "snapshot.json"), snapshot)
}

func (m *Manager) writeSnapshot(path string, snapshot updateSnapshot) error {
	content, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".snapshot-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func (m *Manager) readJournal() (updateSnapshot, error) {
	content, err := os.ReadFile(m.journalPath())
	if err != nil {
		return updateSnapshot{}, fmt.Errorf("read active update transaction: %w", err)
	}
	var snapshot updateSnapshot
	if err := json.Unmarshal(content, &snapshot); err != nil {
		return updateSnapshot{}, err
	}
	backupRoot := filepath.Join(m.updateRoot(), "backups")
	if snapshot.BackupPath == filepath.Clean(backupRoot) || !pathWithin(backupRoot, snapshot.BackupPath) {
		return updateSnapshot{}, fmt.Errorf("update journal contains unsafe backup path")
	}
	if snapshot.OperationID == "" ||
		filepath.Base(snapshot.OperationID) != snapshot.OperationID ||
		filepath.Base(snapshot.BackupPath) != snapshot.OperationID {
		return updateSnapshot{}, fmt.Errorf("update journal contains invalid operation ID")
	}
	releasesRoot := filepath.Join(m.config.InstallDir, "releases")
	if snapshot.PreviousTarget != "" {
		resolved := filepath.Join(m.config.InstallDir, snapshot.PreviousTarget)
		if filepath.IsAbs(snapshot.PreviousTarget) || !pathWithin(releasesRoot, resolved) {
			return updateSnapshot{}, fmt.Errorf("update journal contains unsafe previous release pointer")
		}
	}
	if snapshot.TargetReleasePath != "" &&
		(snapshot.TargetReleasePath == filepath.Clean(releasesRoot) ||
			!pathWithin(releasesRoot, snapshot.TargetReleasePath)) {
		return updateSnapshot{}, fmt.Errorf("update journal contains unsafe target release path")
	}
	bundledParent := filepath.Join(m.config.InstallDir, "script-registry")
	if snapshot.OldBundledPath != "" &&
		(!pathWithin(bundledParent, snapshot.OldBundledPath) ||
			!strings.HasPrefix(filepath.Base(snapshot.OldBundledPath), ".bundled.old-")) {
		return updateSnapshot{}, fmt.Errorf("update journal contains unsafe bundled backup path")
	}
	allowedManaged := map[string]struct{}{
		filepath.Join(m.config.InstallDir, "config.yaml"):    {},
		filepath.Join(m.config.InstallDir, "myadmin.db"):     {},
		filepath.Join(m.config.InstallDir, "myadmin.db-wal"): {},
		filepath.Join(m.config.InstallDir, "myadmin.db-shm"): {},
	}
	for _, live := range snapshot.ManagedFiles {
		if _, ok := allowedManaged[live]; !ok {
			return updateSnapshot{}, fmt.Errorf("update journal contains unsafe managed file")
		}
	}
	for live, backup := range snapshot.Files {
		if _, ok := allowedManaged[live]; !ok && live != filepath.Join(m.config.InstallDir, "one") {
			return updateSnapshot{}, fmt.Errorf("update journal contains unsafe live file")
		}
		if !pathWithin(snapshot.BackupPath, backup) {
			return updateSnapshot{}, fmt.Errorf("update journal contains unsafe backup file")
		}
	}
	return snapshot, nil
}

func (m *Manager) cleanupBackups() {
	root := filepath.Join(m.updateRoot(), "backups")
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	directories := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			directories = append(directories, entry.Name())
		}
	}
	sort.Strings(directories)
	if len(directories) <= m.config.BackupRetention {
		return
	}
	for _, name := range directories[:len(directories)-m.config.BackupRetention] {
		path := filepath.Join(root, name)
		if pathWithin(root, path) {
			_ = os.RemoveAll(path)
		}
	}
}
