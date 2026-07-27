package databasetask

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeDatabaseOperator struct {
	mu           sync.Mutex
	backupCalls  int
	restoreCalls int
	blockBackup  bool
}

func (f *fakeDatabaseOperator) Backup(
	ctx context.Context,
	libraryID int64,
	destination string,
	_ io.Writer,
	report ProgressReporter,
) error {
	f.mu.Lock()
	f.backupCalls++
	block := f.blockBackup
	f.mu.Unlock()
	report(20, "exporting")
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	if err := os.WriteFile(destination, []byte("verified database backup"), 0600); err != nil {
		return err
	}
	report(100, "done")
	return nil
}

func (f *fakeDatabaseOperator) Restore(
	ctx context.Context,
	_ int64,
	source string,
	_ io.Writer,
	report ProgressReporter,
) error {
	f.mu.Lock()
	f.restoreCalls++
	f.mu.Unlock()
	report(30, "restoring")
	if _, err := os.ReadFile(source); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	report(100, "done")
	return nil
}

func TestBackupAndRestoreCreateVerifiedDurableArtifacts(t *testing.T) {
	database := openDatabaseTaskTestDB(t)
	operator := &fakeDatabaseOperator{}
	root := t.TempDir()
	manager := NewManager(
		database,
		filepath.Join(root, "backups"),
		filepath.Join(root, "logs"),
		operator,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})

	backupTask, err := manager.SubmitBackup(11, 1)
	if err != nil {
		t.Fatalf("submit backup: %v", err)
	}
	backupTask = waitForDatabaseTask(t, manager, backupTask.ID)
	if backupTask.Status != models.DatabaseTaskStatusSucceeded ||
		backupTask.ResultBackupID == "" {
		t.Fatalf("unexpected backup task: %+v", backupTask)
	}
	backup, err := manager.GetBackup(backupTask.ResultBackupID)
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}
	if backup.SizeBytes <= 0 || len(backup.SHA256) != 64 || backup.FilePath == "" {
		t.Fatalf("invalid backup metadata: %+v", backup)
	}
	file, _, _, err := manager.OpenBackup(backup.ID)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	file.Close()

	restoreTask, err := manager.SubmitRestore(11, backup.ID, 1)
	if err != nil {
		t.Fatalf("submit restore: %v", err)
	}
	restoreTask = waitForDatabaseTask(t, manager, restoreTask.ID)
	if restoreTask.Status != models.DatabaseTaskStatusSucceeded ||
		restoreTask.SafetyBackupID == "" {
		t.Fatalf("unexpected restore task: %+v", restoreTask)
	}
	safety, err := manager.GetBackup(restoreTask.SafetyBackupID)
	if err != nil {
		t.Fatalf("get safety backup: %v", err)
	}
	if safety.Source != models.DatabaseBackupSourcePreRestore {
		t.Fatalf("safety backup source = %q", safety.Source)
	}
	operator.mu.Lock()
	defer operator.mu.Unlock()
	if operator.backupCalls != 2 || operator.restoreCalls != 1 {
		t.Fatalf("calls: backup=%d restore=%d", operator.backupCalls, operator.restoreCalls)
	}
}

func TestCancelRunningDatabaseTask(t *testing.T) {
	database := openDatabaseTaskTestDB(t)
	operator := &fakeDatabaseOperator{blockBackup: true}
	root := t.TempDir()
	manager := NewManager(
		database,
		filepath.Join(root, "backups"),
		filepath.Join(root, "logs"),
		operator,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	task, err := manager.SubmitBackup(11, 1)
	if err != nil {
		t.Fatal(err)
	}
	waitForDatabaseTaskStatus(t, manager, task.ID, models.DatabaseTaskStatusRunning)
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}
	task = waitForDatabaseTask(t, manager, task.ID)
	if task.Status != models.DatabaseTaskStatusCanceled {
		t.Fatalf("status = %s, want canceled", task.Status)
	}
}

func TestCorruptedBackupIsRejected(t *testing.T) {
	database := openDatabaseTaskTestDB(t)
	operator := &fakeDatabaseOperator{}
	root := t.TempDir()
	manager := NewManager(
		database,
		filepath.Join(root, "backups"),
		filepath.Join(root, "logs"),
		operator,
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	task, err := manager.SubmitBackup(11, 1)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForDatabaseTask(t, manager, task.ID)
	backup, err := manager.GetBackup(task.ResultBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup.FilePath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.OpenBackup(backup.ID); err == nil {
		t.Fatal("expected corrupted backup to be rejected")
	}
	if _, err := manager.SubmitRestore(11, backup.ID, 1); err != nil {
		t.Fatalf("submit queues metadata-valid source: %v", err)
	}
}

func TestStartMarksAbandonedTaskInterrupted(t *testing.T) {
	database := openDatabaseTaskTestDB(t)
	now := time.Now().UTC()
	task := &models.DatabaseTask{
		ID: "abandoned", Operation: "backup", LibraryID: 11,
		DatabaseName: "appdb", Status: models.DatabaseTaskStatusRunning,
		RequestedBy: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.DatabaseOperationLock{
		LibraryID: 11, TaskID: task.ID, AcquiredAt: now, HeartbeatAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	manager := NewManager(
		database,
		filepath.Join(root, "backups"),
		filepath.Join(root, "logs"),
		&fakeDatabaseOperator{},
	)
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	reconciled, err := manager.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.Status != models.DatabaseTaskStatusInterrupted {
		t.Fatalf("status = %s, want interrupted", reconciled.Status)
	}
	var locks int64
	if err := database.Model(&models.DatabaseOperationLock{}).Count(&locks).Error; err != nil {
		t.Fatal(err)
	}
	if locks != 0 {
		t.Fatalf("stale locks = %d, want 0", locks)
	}
}

func openDatabaseTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tasks.db")
	database, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(
		&models.Storage{},
		&models.Library{},
		&models.DatabaseTask{},
		&models.DatabaseBackup{},
		&models.DatabaseOperationLock{},
	); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Storage{
		ID: 7, Addr: "127.0.0.1", Port: "3306", Root: "root", Type: "mysql",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Library{
		ID: 11, PID: 7, Name: "appdb", Type: "mysql",
	}).Error; err != nil {
		t.Fatal(err)
	}
	return database
}

func waitForDatabaseTask(t *testing.T, manager *Manager, taskID string) *models.DatabaseTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if models.IsDatabaseTaskTerminal(task.Status) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not finish", taskID)
	return nil
}

func waitForDatabaseTaskStatus(t *testing.T, manager *Manager, taskID, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.GetTask(taskID)
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatal(err)
		}
		if err == nil && task.Status == status {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("task %s did not reach %s", taskID, status)
}
