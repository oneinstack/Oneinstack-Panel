package websitetask

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/databasetask"
	"oneinstack/internal/services/website"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type fakeCommandRunner struct{}

func (fakeCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

type fakeDatabaseOperator struct {
	mu          sync.Mutex
	backupValue []byte
	restored    []byte
	block       chan struct{}
}

func (operator *fakeDatabaseOperator) Backup(
	ctx context.Context,
	_ int64,
	destination string,
	_ io.Writer,
	report databasetask.ProgressReporter,
) error {
	if operator.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-operator.block:
		}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return err
	}
	report(50, "fake database backup")
	operator.mu.Lock()
	value := append([]byte(nil), operator.backupValue...)
	operator.mu.Unlock()
	return os.WriteFile(destination, value, 0600)
}

func (operator *fakeDatabaseOperator) Restore(
	ctx context.Context,
	_ int64,
	source string,
	_ io.Writer,
	report databasetask.ProgressReporter,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	value, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	operator.mu.Lock()
	operator.restored = append([]byte(nil), value...)
	operator.mu.Unlock()
	report(100, "fake database restore")
	return nil
}

func TestWebsiteBackupRestoreDeleteLifecycle(t *testing.T) {
	db := openWebsiteTaskTestDB(t)
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	logRoot := filepath.Join(root, "wwwlogs")
	configRoot := filepath.Join(root, "nginx")
	certificateRoot := filepath.Join(root, "certificates")
	for _, directory := range []string{webRoot, logRoot, configRoot, certificateRoot} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	siteService := &website.Service{
		DB: db, WebRoot: webRoot, LogRoot: logRoot,
		ChallengeRoot:   filepath.Join(root, "challenge"),
		CertificateRoot: certificateRoot,
		Publisher: &website.Publisher{
			ConfigDir: configRoot, NginxBinary: "nginx", Runner: fakeCommandRunner{},
		},
	}
	if err := os.MkdirAll(siteService.ChallengeRoot, 0750); err != nil {
		t.Fatal(err)
	}
	site := &models.Website{
		Domain: "example.com", Type: "static", RootDir: "/example.com",
		Remark: "production",
	}
	if err := siteService.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(site.RootDir, "index.html")
	if err := os.WriteFile(indexPath, []byte("version-one"), 0640); err != nil {
		t.Fatal(err)
	}
	connection := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root", Type: "mysql",
	}
	if err := db.Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	library := &models.Library{
		PID: connection.ID, Name: "site_db", Type: "mysql",
	}
	if err := db.Create(library).Error; err != nil {
		t.Fatal(err)
	}
	databaseOperator := &fakeDatabaseOperator{backupValue: []byte("database-version-one")}
	manager := NewManager(
		db,
		filepath.Join(root, "backups"),
		filepath.Join(root, "task-logs"),
		siteService,
		databaseOperator,
		64<<20,
		1000,
		0,
	)
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})

	backupTask, err := manager.SubmitBackup(site.ID, library.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	backupTask = waitForWebsiteTask(t, manager, backupTask.ID)
	if backupTask.Status != models.WebsiteTaskStatusSucceeded ||
		backupTask.ResultBackupID == "" {
		t.Fatalf("unexpected backup task: %#v", backupTask)
	}
	backup, err := manager.GetBackup(backupTask.ResultBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if backup.DatabaseName != "site_db" || backup.SHA256 == "" || backup.SizeBytes == 0 {
		t.Fatalf("unexpected website backup: %#v", backup)
	}

	if err := os.WriteFile(indexPath, []byte("version-two"), 0640); err != nil {
		t.Fatal(err)
	}
	databaseOperator.backupValue = []byte("database-before-restore")
	restoreTask, err := manager.SubmitRestore(backup.ID, site.Name, 1)
	if err != nil {
		t.Fatal(err)
	}
	restoreTask = waitForWebsiteTask(t, manager, restoreTask.ID)
	if restoreTask.Status != models.WebsiteTaskStatusSucceeded ||
		restoreTask.SafetyBackupID == "" {
		t.Fatalf("unexpected restore task: %#v", restoreTask)
	}
	value, err := os.ReadFile(indexPath)
	if err != nil || string(value) != "version-one" {
		t.Fatalf("website file was not restored: %q, %v", value, err)
	}
	databaseOperator.mu.Lock()
	restoredDatabase := string(databaseOperator.restored)
	databaseOperator.mu.Unlock()
	if restoredDatabase != "database-version-one" {
		t.Fatalf("database restore value = %q", restoredDatabase)
	}

	databaseOperator.backupValue = []byte("database-before-delete")
	deleteTask, err := manager.SubmitDelete(site.ID, library.ID, true, site.Name, 1)
	if err != nil {
		t.Fatal(err)
	}
	deleteTask = waitForWebsiteTask(t, manager, deleteTask.ID)
	if deleteTask.Status != models.WebsiteTaskStatusSucceeded ||
		deleteTask.ResultBackupID == "" {
		t.Fatalf("unexpected delete task: %#v", deleteTask)
	}
	if _, err := os.Stat(site.RootDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("website files were not deleted after verified snapshot: %v", err)
	}
	var siteCount int64
	if err := db.Model(&models.Website{}).Where("id = ?", site.ID).Count(&siteCount).Error; err != nil {
		t.Fatal(err)
	}
	if siteCount != 0 {
		t.Fatalf("website row remains after delete: %d", siteCount)
	}

	deleteBackup, err := manager.GetBackup(deleteTask.ResultBackupID)
	if err != nil {
		t.Fatal(err)
	}
	recoverTask, err := manager.SubmitRestore(deleteBackup.ID, site.Name, 1)
	if err != nil {
		t.Fatal(err)
	}
	recoverTask = waitForWebsiteTask(t, manager, recoverTask.ID)
	if recoverTask.Status != models.WebsiteTaskStatusSucceeded {
		t.Fatalf("unexpected recovery task: %#v", recoverTask)
	}
	value, err = os.ReadFile(indexPath)
	if err != nil || string(value) != "version-one" {
		t.Fatalf("deleted website was not recovered: %q, %v", value, err)
	}
}

func TestWebsiteTaskCancellationAndRestartRecovery(t *testing.T) {
	db := openWebsiteTaskTestDB(t)
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	for _, directory := range []string{
		webRoot,
		filepath.Join(root, "logs"),
		filepath.Join(root, "nginx"),
		filepath.Join(root, "challenge"),
		filepath.Join(root, "certificates"),
	} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	service := &website.Service{
		DB: db, WebRoot: webRoot, LogRoot: filepath.Join(root, "logs"),
		ChallengeRoot:   filepath.Join(root, "challenge"),
		CertificateRoot: filepath.Join(root, "certificates"),
		Publisher: &website.Publisher{
			ConfigDir:   filepath.Join(root, "nginx"),
			NginxBinary: "nginx", Runner: fakeCommandRunner{},
		},
	}
	site := &models.Website{Domain: "cancel.example.com", Type: "static", RootDir: "/cancel"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	operator := &fakeDatabaseOperator{backupValue: []byte("db"), block: block}
	connection := &models.Storage{Addr: "127.0.0.1", Port: "3306", Root: "root", Type: "mysql"}
	if err := db.Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	library := &models.Library{PID: connection.ID, Name: "cancel_db", Type: "mysql"}
	if err := db.Create(library).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewManager(
		db, filepath.Join(root, "backups"), filepath.Join(root, "tasklogs"),
		service, operator, 64<<20, 1000, 0,
	)
	task, err := manager.SubmitBackup(site.ID, library.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	waitForWebsiteTaskStatus(t, manager, task.ID, models.WebsiteTaskStatusRunning)
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	task = waitForWebsiteTask(t, manager, task.ID)
	if task.Status != models.WebsiteTaskStatusCanceled {
		t.Fatalf("canceled task status = %s", task.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	stale := &models.WebsiteTask{
		ID: "stale-task", Operation: models.WebsiteTaskOperationBackup,
		WebsiteID: site.ID, WebsiteName: site.Name,
		Status: models.WebsiteTaskStatusRunning, RequestedBy: 1,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := db.Create(stale).Error; err != nil {
		t.Fatal(err)
	}
	restarted := NewManager(
		db, filepath.Join(root, "backups-2"), filepath.Join(root, "tasklogs-2"),
		service, &fakeDatabaseOperator{}, 64<<20, 1000, 0,
	)
	if err := restarted.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = restarted.Stop(ctx)
	})
	recovered, err := restarted.GetTask(stale.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != models.WebsiteTaskStatusInterrupted ||
		recovered.ErrorCode != "PANEL_RESTARTED" {
		t.Fatalf("unexpected recovered task: %#v", recovered)
	}
}

func TestExtractArchiveRejectsTraversalAndUnsafeSymlink(t *testing.T) {
	for name, addEntry := range map[string]func(*tar.Writer) error{
		"traversal": func(writer *tar.Writer) error {
			return writer.WriteHeader(&tar.Header{
				Name: "../escape", Typeflag: tar.TypeReg, Mode: 0600, Size: 0,
			})
		},
		"absolute-symlink": func(writer *tar.Writer) error {
			return writer.WriteHeader(&tar.Header{
				Name: "site/link", Typeflag: tar.TypeSymlink, Mode: 0777, Linkname: "/etc/passwd",
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			archivePath := filepath.Join(t.TempDir(), "malicious.tar.gz")
			file, err := os.Create(archivePath)
			if err != nil {
				t.Fatal(err)
			}
			gzipWriter := gzip.NewWriter(file)
			tarWriter := tar.NewWriter(gzipWriter)
			if err := addEntry(tarWriter); err != nil {
				t.Fatal(err)
			}
			if err := tarWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = extractArchive(
				context.Background(), archivePath, t.TempDir(),
				archiveLimits{MaxBytes: 1 << 20, MaxFiles: 100},
			)
			if err == nil {
				t.Fatal("malicious website archive was accepted")
			}
		})
	}
}

func TestDeleteBackupRejectsTamperedArtifact(t *testing.T) {
	db := openWebsiteTaskTestDB(t)
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	for _, directory := range []string{
		webRoot, filepath.Join(root, "logs"), filepath.Join(root, "nginx"),
		filepath.Join(root, "challenge"), filepath.Join(root, "certificates"),
	} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	service := &website.Service{
		DB: db, WebRoot: webRoot, LogRoot: filepath.Join(root, "logs"),
		ChallengeRoot:   filepath.Join(root, "challenge"),
		CertificateRoot: filepath.Join(root, "certificates"),
		Publisher: &website.Publisher{
			ConfigDir:   filepath.Join(root, "nginx"),
			NginxBinary: "nginx", Runner: fakeCommandRunner{},
		},
	}
	site := &models.Website{Domain: "tamper.example.com", Type: "static", RootDir: "/tamper"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(
		db, filepath.Join(root, "backups"), filepath.Join(root, "tasklogs"),
		service, &fakeDatabaseOperator{}, 64<<20, 1000, 0,
	)
	task, err := manager.SubmitBackup(site.ID, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	task = waitForWebsiteTask(t, manager, task.ID)
	backup, err := manager.GetBackup(task.ResultBackupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backup.FilePath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := manager.OpenBackup(backup.ID); err == nil ||
		!strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered website backup was not rejected: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = manager.Stop(ctx)
}

func TestWebsiteBackupCleanerRetainsBackupUsedByActiveRestore(t *testing.T) {
	db := openWebsiteTaskTestDB(t)
	root := t.TempDir()
	for _, directory := range []string{
		filepath.Join(root, "www"), filepath.Join(root, "logs"),
		filepath.Join(root, "nginx"), filepath.Join(root, "challenge"),
		filepath.Join(root, "certificates"),
	} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	service := &website.Service{
		DB: db, WebRoot: filepath.Join(root, "www"), LogRoot: filepath.Join(root, "logs"),
		ChallengeRoot: filepath.Join(root, "challenge"),
		CertificateRoot: filepath.Join(root, "certificates"),
		Publisher: &website.Publisher{
			ConfigDir: filepath.Join(root, "nginx"),
			NginxBinary: "nginx", Runner: fakeCommandRunner{},
		},
	}
	manager := NewManager(
		db, filepath.Join(root, "backups"), filepath.Join(root, "tasklogs"),
		service, &fakeDatabaseOperator{}, 64<<20, 1000, 0,
	)
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = manager.Stop(ctx)
	})
	backupID := uuid.NewString()
	path, err := manager.artifactPath(7, backupID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("expired artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	size, checksum, err := verifyRegularFile(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	backup := &models.WebsiteBackup{
		ID: backupID, WebsiteID: 7, WebsiteName: "expired.example.com",
		Source: models.WebsiteBackupSourceManual,
		FileName: "expired.tar.gz", FilePath: path,
		SizeBytes: size, SHA256: checksum, CreatedBy: 1,
		CreatedAt: now.AddDate(0, 0, -40),
	}
	if err := db.Create(backup).Error; err != nil {
		t.Fatal(err)
	}
	task := &models.WebsiteTask{
		ID: uuid.NewString(), Operation: models.WebsiteTaskOperationRestore,
		WebsiteID: 7, WebsiteName: backup.WebsiteName,
		SourceBackupID: backup.ID, Status: models.WebsiteTaskStatusRunning,
		RequestedBy: 1, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	cleaner, err := NewCleaner(manager, 30, "0 4 * * *")
	if err != nil {
		t.Fatal(err)
	}
	cleaner.now = func() time.Time { return now }
	result, err := cleaner.RunNow()
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupsDeleted != 0 {
		t.Fatal("backup used by active restore was deleted")
	}
	if err := db.Model(&models.WebsiteTask{}).Where("id = ?", task.ID).
		Update("status", models.WebsiteTaskStatusSucceeded).Error; err != nil {
		t.Fatal(err)
	}
	result, err = cleaner.RunNow()
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupsDeleted != 1 {
		t.Fatalf("expired backup cleanup result: %#v", result)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired backup file remains: %v", err)
	}
}

func waitForWebsiteTask(t *testing.T, manager *Manager, taskID string) *models.WebsiteTask {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if models.IsWebsiteTaskTerminal(task.Status) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("website task %s did not finish", taskID)
	return nil
}

func waitForWebsiteTaskStatus(t *testing.T, manager *Manager, taskID, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == status {
			return
		}
		if models.IsWebsiteTaskTerminal(task.Status) {
			t.Fatalf("website task reached %s before %s: %#v", task.Status, status, task)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("website task %s did not reach %s", taskID, status)
}

func openWebsiteTaskTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "website-task.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Website{},
		&models.Storage{},
		&models.Library{},
		&models.DatabaseTask{},
		&models.DatabaseBackup{},
		&models.DatabaseOperationLock{},
		&models.Certificate{},
		&models.CertificateTask{},
		&models.CertificateOperationLock{},
		&models.WebsiteTask{},
		&models.WebsiteBackup{},
		&models.WebsiteOperationLock{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}
