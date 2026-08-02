package websitetask

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/databasetask"
	"oneinstack/internal/services/website"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/disk"
	"gorm.io/gorm"
)

const (
	defaultQueueSize = 32
	defaultWorkers   = 2
)

type DatabaseOperator interface {
	Backup(
		context.Context,
		int64,
		string,
		io.Writer,
		databasetask.ProgressReporter,
	) error
	Restore(
		context.Context,
		int64,
		string,
		io.Writer,
		databasetask.ProgressReporter,
	) error
}

type Request struct {
	Operation   string
	WebsiteID   int64
	BackupID    string
	DatabaseID  int64
	DeleteFiles bool
	ConfirmName string
}

type queuedTask struct {
	taskID  string
	request Request
}

type Manager struct {
	db               *gorm.DB
	backupRoot       string
	logRoot          string
	sites            *website.Service
	databases        DatabaseOperator
	limits           archiveLimits
	minimumFreeBytes int64
	queue            chan queuedTask
	stopCh           chan struct{}

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	submitMu  sync.Mutex
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc
	runWG     sync.WaitGroup
	stopping  atomic.Bool
}

func NewManager(
	db *gorm.DB,
	backupRoot, logRoot string,
	sites *website.Service,
	databases DatabaseOperator,
	maxBytes int64,
	maxFiles int,
	minimumFreeBytes int64,
) *Manager {
	return &Manager{
		db: db, backupRoot: filepath.Clean(backupRoot), logRoot: filepath.Clean(logRoot),
		sites: sites, databases: databases,
		limits:           archiveLimits{MaxBytes: maxBytes, MaxFiles: maxFiles},
		minimumFreeBytes: minimumFreeBytes,
		queue:            make(chan queuedTask, defaultQueueSize), stopCh: make(chan struct{}),
		cancels: make(map[string]context.CancelFunc),
	}
}

func (m *Manager) Start() error {
	m.startOnce.Do(func() {
		switch {
		case m.db == nil:
			m.startErr = errors.New("website task database is not initialized")
		case m.sites == nil:
			m.startErr = errors.New("website service is not configured")
		case m.databases == nil:
			m.startErr = errors.New("website database operator is not configured")
		case invalidRoot(m.backupRoot):
			m.startErr = errors.New("website backup directory is invalid")
		case invalidRoot(m.logRoot):
			m.startErr = errors.New("website task log directory is invalid")
		case m.limits.MaxBytes < 1<<20 || m.limits.MaxFiles < 1:
			m.startErr = errors.New("website backup limits are invalid")
		}
		if m.startErr != nil {
			return
		}
		if err := os.MkdirAll(m.backupRoot, 0750); err != nil {
			m.startErr = fmt.Errorf("create website backup directory: %w", err)
			return
		}
		if err := ensureRealDirectory(m.backupRoot); err != nil {
			m.startErr = fmt.Errorf("validate website backup directory: %w", err)
			return
		}
		if err := os.MkdirAll(m.logRoot, 0750); err != nil {
			m.startErr = fmt.Errorf("create website task log directory: %w", err)
			return
		}
		if err := ensureRealDirectory(m.logRoot); err != nil {
			m.startErr = fmt.Errorf("validate website task log directory: %w", err)
			return
		}
		if err := m.reconcileInterruptedTasks(); err != nil {
			m.startErr = err
			return
		}
		for i := 0; i < defaultWorkers; i++ {
			go m.worker()
		}
	})
	return m.startErr
}

func invalidRoot(path string) bool {
	return path == "" || path == "." || path == string(filepath.Separator) || !filepath.IsAbs(path)
}

func (m *Manager) SubmitBackup(websiteID, databaseID, requestedBy int64) (*models.WebsiteTask, error) {
	return m.submit(Request{
		Operation: models.WebsiteTaskOperationBackup,
		WebsiteID: websiteID, DatabaseID: databaseID,
	}, requestedBy)
}

func (m *Manager) SubmitRestore(
	backupID, confirmName string,
	requestedBy int64,
) (*models.WebsiteTask, error) {
	return m.submit(Request{
		Operation: models.WebsiteTaskOperationRestore,
		BackupID:  strings.TrimSpace(backupID), ConfirmName: strings.TrimSpace(confirmName),
	}, requestedBy)
}

func (m *Manager) SubmitDelete(
	websiteID, databaseID int64,
	deleteFiles bool,
	confirmName string,
	requestedBy int64,
) (*models.WebsiteTask, error) {
	return m.submit(Request{
		Operation: models.WebsiteTaskOperationDelete,
		WebsiteID: websiteID, DatabaseID: databaseID,
		DeleteFiles: deleteFiles, ConfirmName: strings.TrimSpace(confirmName),
	}, requestedBy)
}

func (m *Manager) submit(request Request, requestedBy int64) (*models.WebsiteTask, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	if m.stopping.Load() {
		return nil, errors.New("website task manager is stopping")
	}
	if requestedBy <= 0 {
		return nil, errors.New("authenticated user is required")
	}
	var site *models.Website
	var sourceBackup *models.WebsiteBackup
	var err error
	switch request.Operation {
	case models.WebsiteTaskOperationBackup:
		site, err = m.sites.Get(request.WebsiteID)
	case models.WebsiteTaskOperationDelete:
		site, err = m.sites.Get(request.WebsiteID)
		if err == nil && request.ConfirmName != site.Name {
			err = errors.New("网站确认名称不匹配")
		}
	case models.WebsiteTaskOperationRestore:
		if request.BackupID == "" {
			return nil, errors.New("website backup is required")
		}
		sourceBackup, err = m.GetBackup(request.BackupID)
		if err == nil {
			request.WebsiteID = sourceBackup.WebsiteID
			request.DatabaseID = sourceBackup.DatabaseID
			if request.ConfirmName != sourceBackup.WebsiteName {
				err = errors.New("网站确认名称不匹配")
			}
		}
	default:
		return nil, errors.New("unsupported website task operation")
	}
	if err != nil {
		return nil, err
	}
	websiteName := ""
	if sourceBackup != nil {
		websiteName = sourceBackup.WebsiteName
	}
	if site != nil {
		websiteName = site.Name
	}
	databaseName, err := m.validateDatabase(request.DatabaseID)
	if err != nil {
		return nil, err
	}

	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	var active int64
	if err := m.db.Model(&models.WebsiteTask{}).
		Where("website_id = ? AND status IN ?", request.WebsiteID, models.ActiveWebsiteTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, fmt.Errorf("website %s already has an active task", websiteName)
	}
	if request.DatabaseID > 0 {
		if err := m.db.Model(&models.DatabaseTask{}).
			Where("library_id = ? AND status IN ?", request.DatabaseID, models.ActiveDatabaseTaskStatuses()).
			Count(&active).Error; err != nil {
			return nil, err
		}
		if active > 0 {
			return nil, fmt.Errorf("database %s already has an active task", databaseName)
		}
	}
	taskID := uuid.NewString()
	now := time.Now().UTC()
	task := &models.WebsiteTask{
		ID: taskID, Operation: request.Operation,
		WebsiteID: request.WebsiteID, WebsiteName: websiteName,
		DatabaseID: request.DatabaseID, DatabaseName: databaseName,
		SourceBackupID: request.BackupID, DeleteFiles: request.DeleteFiles,
		Status: models.WebsiteTaskStatusQueued, Progress: 0,
		Message: "网站任务已进入队列", RequestedBy: requestedBy,
		LogPath:   filepath.Join(m.logRoot, "task_"+taskID+".log"),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := m.db.Create(task).Error; err != nil {
		return nil, err
	}
	select {
	case m.queue <- queuedTask{taskID: taskID, request: request}:
		return task, nil
	case <-m.stopCh:
		_ = m.finish(task.ID, models.WebsiteTaskStatusInterrupted, "MANAGER_STOPPED", "网站任务服务已停止")
		return nil, errors.New("website task manager is stopping")
	default:
		_ = m.finish(task.ID, models.WebsiteTaskStatusFailed, "QUEUE_FULL", "网站任务队列已满")
		return nil, errors.New("website task queue is full")
	}
}

func (m *Manager) validateDatabase(databaseID int64) (string, error) {
	if databaseID == 0 {
		return "", nil
	}
	if databaseID < 0 {
		return "", errors.New("database ID is invalid")
	}
	var library models.Library
	if err := m.db.First(&library, databaseID).Error; err != nil {
		return "", err
	}
	if library.Type != "mysql" {
		return "", errors.New("website backup currently supports one optional MySQL database")
	}
	return library.Name, nil
}

func (m *Manager) worker() {
	for {
		select {
		case <-m.stopCh:
			return
		case item := <-m.queue:
			if m.stopping.Load() {
				return
			}
			m.runWG.Add(1)
			m.run(item)
			m.runWG.Done()
		}
	}
}

func (m *Manager) run(item queuedTask) {
	var task models.WebsiteTask
	if err := m.db.First(&task, "id = ?", item.taskID).Error; err != nil {
		return
	}
	if models.IsWebsiteTaskTerminal(task.Status) || task.CancelRequested {
		return
	}
	if err := m.acquireLocks(&task); err != nil {
		_ = m.finish(task.ID, models.WebsiteTaskStatusFailed, "RESOURCE_BUSY", err.Error())
		return
	}
	defer m.releaseLocks(&task)

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelMu.Lock()
	m.cancels[task.ID] = cancel
	m.cancelMu.Unlock()
	defer func() {
		cancel()
		m.cancelMu.Lock()
		delete(m.cancels, task.ID)
		m.cancelMu.Unlock()
	}()
	if m.stopping.Load() || m.cancelRequested(task.ID) {
		cancel()
	}
	logFile, err := openTaskLog(task.LogPath)
	if err != nil {
		_ = m.finish(task.ID, models.WebsiteTaskStatusFailed, "LOG_OPEN_FAILED", err.Error())
		return
	}
	defer logFile.Close()
	now := time.Now().UTC()
	_ = m.db.Model(&models.WebsiteTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status": models.WebsiteTaskStatusRunning, "progress": 1,
		"message": "网站任务开始执行", "started_at": now, "heartbeat_at": now,
	}).Error
	_, _ = fmt.Fprintf(logFile, "[%s] %s task started for website %s\n",
		now.Format(time.RFC3339), task.Operation, task.WebsiteName)
	heartbeatDone := make(chan struct{})
	go m.heartbeat(task.ID, task.WebsiteID, task.DatabaseID, heartbeatDone)
	defer close(heartbeatDone)

	report := func(progress int, message string) { m.report(task.ID, progress, message) }
	switch task.Operation {
	case models.WebsiteTaskOperationBackup:
		var backup *models.WebsiteBackup
		backup, err = m.createAndRegisterBackup(
			ctx, &task, models.WebsiteBackupSourceManual, logFile, report,
		)
		if err == nil {
			_ = m.db.Model(&models.WebsiteTask{}).Where("id = ?", task.ID).
				Update("result_backup_id", backup.ID).Error
		}
	case models.WebsiteTaskOperationDelete:
		err = m.runDelete(ctx, &task, logFile, report)
	case models.WebsiteTaskOperationRestore:
		err = m.runRestore(ctx, &task, logFile, report)
	}
	if err != nil {
		status := models.WebsiteTaskStatusFailed
		code := "WEBSITE_OPERATION_FAILED"
		message := err.Error()
		if errors.Is(err, context.Canceled) || m.cancelRequested(task.ID) {
			if m.stopping.Load() {
				status = models.WebsiteTaskStatusInterrupted
				code = "PANEL_STOPPED"
				message = "Panel 停止，网站任务已中断"
			} else {
				status = models.WebsiteTaskStatusCanceled
				code = "TASK_CANCELED"
				message = "网站任务已取消"
			}
		}
		_, _ = fmt.Fprintf(logFile, "[%s] task failed: %s\n", time.Now().UTC().Format(time.RFC3339), message)
		_ = m.finish(task.ID, status, code, message)
		return
	}
	_, _ = fmt.Fprintf(logFile, "[%s] task completed\n", time.Now().UTC().Format(time.RFC3339))
	_ = m.finish(task.ID, models.WebsiteTaskStatusSucceeded, "", "网站任务执行成功")
}

func (m *Manager) runDelete(
	ctx context.Context,
	task *models.WebsiteTask,
	log io.Writer,
	report databasetask.ProgressReporter,
) error {
	report(5, "正在创建删除前强制快照")
	backup, err := m.createAndRegisterBackup(
		ctx, task, models.WebsiteBackupSourcePreDelete, log,
		func(progress int, message string) {
			report(scaleProgress(progress, 5, 80), "删除前快照："+message)
		},
	)
	if err != nil {
		return fmt.Errorf("create mandatory pre-delete backup: %w", err)
	}
	if err := m.db.Model(&models.WebsiteTask{}).Where("id = ?", task.ID).
		Update("result_backup_id", backup.ID).Error; err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	report(85, "快照已验证，正在删除网站配置")
	if err := m.sites.DeleteWithOptions(ctx, task.WebsiteID, task.DeleteFiles); err != nil {
		return err
	}
	report(99, "网站已安全删除，恢复快照已保留")
	return nil
}

func (m *Manager) runRestore(
	ctx context.Context,
	task *models.WebsiteTask,
	log io.Writer,
	report databasetask.ProgressReporter,
) error {
	source, sourcePath, err := m.verifiedBackup(task.SourceBackupID)
	if err != nil {
		return fmt.Errorf("verify website backup: %w", err)
	}
	if source.WebsiteID != task.WebsiteID || source.WebsiteName != task.WebsiteName {
		return errors.New("website backup metadata does not match task")
	}
	restoreParent := filepath.Join(m.sites.WebRoot, ".oneinstack-restore")
	if err := os.MkdirAll(restoreParent, 0700); err != nil {
		return err
	}
	if err := ensureRealDirectory(restoreParent); err != nil {
		return err
	}
	restoreRoot := filepath.Join(restoreParent, task.ID)
	if err := os.Mkdir(restoreRoot, 0700); err != nil {
		return fmt.Errorf("create website restore staging directory: %w", err)
	}
	defer os.RemoveAll(restoreRoot)
	report(5, "正在校验并解压网站备份")
	extracted, err := extractArchive(ctx, sourcePath, restoreRoot, m.limits)
	if err != nil {
		return err
	}
	if extracted.Manifest.Website.ID != source.WebsiteID ||
		extracted.Manifest.Website.Name != source.WebsiteName {
		return errors.New("website archive manifest does not match backup metadata")
	}
	var previous *models.Website
	var previousSettings *website.WebsiteSettings
	previous, err = m.sites.Get(task.WebsiteID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		previous = nil
	} else if err != nil {
		return err
	}
	if previous != nil {
		settingsDocument, settingsErr := m.sites.GetSettings(previous.ID)
		if settingsErr != nil {
			return fmt.Errorf("read current website settings: %w", settingsErr)
		}
		settingsCopy := settingsDocument.Settings
		previousSettings = &settingsCopy
		report(20, "正在创建恢复前安全快照")
		safety, err := m.createAndRegisterBackup(
			ctx, task, models.WebsiteBackupSourcePreRestore, log,
			func(progress int, message string) {
				report(scaleProgress(progress, 20, 48), "恢复前快照："+message)
			},
		)
		if err != nil {
			return fmt.Errorf("create pre-restore safety backup: %w", err)
		}
		if err := m.db.Model(&models.WebsiteTask{}).Where("id = ?", task.ID).
			Update("safety_backup_id", safety.ID).Error; err != nil {
			return err
		}
	}
	report(50, "正在恢复网站文件")
	rollbackFiles, commitFiles, err := m.replaceWebsiteFiles(
		&extracted.Manifest.Website,
		extracted.SiteRoot,
		restoreRoot,
	)
	if err != nil {
		return err
	}
	rollbackNeeded := true
	defer func() {
		if rollbackNeeded {
			if previous != nil {
				_ = m.sites.RestoreSnapshot(context.Background(), previous)
				if previousSettings != nil {
					_, _ = m.sites.UpdateSettings(
						context.Background(), previous.ID, *previousSettings,
					)
				}
			} else {
				_ = m.sites.DeleteWithOptions(context.Background(), task.WebsiteID, false)
			}
			_ = rollbackFiles()
		}
	}()
	report(68, "正在重新生成并发布 Nginx 配置")
	if err := m.sites.RestoreSnapshot(ctx, &extracted.Manifest.Website); err != nil {
		return err
	}
	if extracted.Manifest.WebsiteSettings != nil {
		if _, err := m.sites.UpdateSettings(
			ctx,
			extracted.Manifest.Website.ID,
			*extracted.Manifest.WebsiteSettings,
		); err != nil {
			return fmt.Errorf("restore website settings: %w", err)
		}
	}
	if extracted.Manifest.Database != nil {
		if extracted.Manifest.Database.ID != task.DatabaseID ||
			extracted.Manifest.Database.Name != task.DatabaseName {
			return errors.New("website database metadata does not match task")
		}
		report(75, "正在恢复关联数据库")
		if err := m.databases.Restore(
			ctx, task.DatabaseID, extracted.DatabasePath, log,
			func(progress int, message string) {
				report(scaleProgress(progress, 75, 96), message)
			},
		); err != nil {
			return fmt.Errorf("restore website database: %w", err)
		}
	}
	if err := commitFiles(); err != nil {
		return err
	}
	rollbackNeeded = false
	report(99, "网站文件、配置和数据库恢复完成")
	return nil
}

func (m *Manager) replaceWebsiteFiles(
	site *models.Website,
	stagedSiteRoot, operationRoot string,
) (func() error, func() error, error) {
	target, err := m.sites.ManagedRoot(site)
	if err != nil {
		return nil, nil, err
	}
	if target == "" {
		return func() error { return nil }, func() error { return nil }, nil
	}
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return nil, nil, err
	}
	previous := filepath.Join(operationRoot, "previous")
	hadPrevious := false
	if info, statErr := os.Lstat(target); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, errors.New("existing website root is not a real directory")
		}
		if err := os.Rename(target, previous); err != nil {
			return nil, nil, err
		}
		hadPrevious = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, nil, statErr
	}
	if err := os.Rename(stagedSiteRoot, target); err != nil {
		if hadPrevious {
			_ = os.Rename(previous, target)
		}
		return nil, nil, err
	}
	rollback := func() error {
		var result error
		failed := target + ".failed-" + uuid.NewString()
		if err := os.Rename(target, failed); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
		if hadPrevious {
			result = errors.Join(result, os.Rename(previous, target))
		}
		if err := os.RemoveAll(failed); err != nil {
			result = errors.Join(result, err)
		}
		return result
	}
	commit := func() error {
		if !hadPrevious {
			return nil
		}
		return os.RemoveAll(previous)
	}
	return rollback, commit, nil
}

func (m *Manager) createAndRegisterBackup(
	ctx context.Context,
	task *models.WebsiteTask,
	source string,
	log io.Writer,
	report databasetask.ProgressReporter,
) (*models.WebsiteBackup, error) {
	site, err := m.sites.Get(task.WebsiteID)
	if err != nil {
		return nil, err
	}
	rootPath, err := m.sites.ManagedRoot(site)
	if err != nil {
		return nil, err
	}
	configPath, err := m.sites.ConfigFile(site)
	if err != nil {
		return nil, err
	}
	settingsDocument, err := m.sites.GetSettings(site.ID)
	if err != nil {
		return nil, fmt.Errorf("read website settings: %w", err)
	}
	settings := settingsDocument.Settings
	if err := m.checkDiskSpace(); err != nil {
		return nil, err
	}
	workParent := filepath.Join(m.backupRoot, ".work")
	if err := os.MkdirAll(workParent, 0700); err != nil {
		return nil, err
	}
	if err := ensureRealDirectory(workParent); err != nil {
		return nil, err
	}
	workRoot := filepath.Join(workParent, task.ID+"-"+uuid.NewString())
	if err := os.Mkdir(workRoot, 0700); err != nil {
		return nil, err
	}
	defer os.RemoveAll(workRoot)
	var dump *databaseDump
	if task.DatabaseID > 0 {
		dumpPath := filepath.Join(workRoot, "database.sql.gz")
		report(5, "正在导出关联数据库")
		if err := m.databases.Backup(
			ctx, task.DatabaseID, dumpPath, log,
			func(progress int, message string) {
				report(scaleProgress(progress, 5, 45), message)
			},
		); err != nil {
			return nil, fmt.Errorf("backup website database: %w", err)
		}
		dump = &databaseDump{ID: task.DatabaseID, Name: task.DatabaseName, Path: dumpPath}
	}
	backupID := uuid.NewString()
	artifact, err := m.artifactPath(task.WebsiteID, backupID)
	if err != nil {
		return nil, err
	}
	report(50, "正在打包网站文件与配置")
	_, size, checksum, err := buildArchive(
		ctx, site, &settings, rootPath, configPath, dump, artifact, m.limits,
	)
	if err != nil {
		return nil, err
	}
	backup := &models.WebsiteBackup{
		ID: backupID, WebsiteID: site.ID, WebsiteName: site.Name,
		DatabaseID: task.DatabaseID, DatabaseName: task.DatabaseName,
		Source:   source,
		FileName: site.Name + "_" + time.Now().UTC().Format("20060102_150405") + ".tar.gz",
		FilePath: artifact, SizeBytes: size, SHA256: checksum,
		CreatedBy: task.RequestedBy, CreatedAt: time.Now().UTC(),
	}
	if err := m.db.Create(backup).Error; err != nil {
		_ = os.Remove(artifact)
		return nil, err
	}
	report(100, "网站备份已完成并通过完整性校验")
	return backup, nil
}

func (m *Manager) checkDiskSpace() error {
	usage, err := disk.Usage(m.backupRoot)
	if err != nil {
		return fmt.Errorf("read website backup disk capacity: %w", err)
	}
	minimum := m.minimumFreeBytes
	if minimum < 0 {
		minimum = 0
	}
	const headroom = uint64(1 << 20)
	required := uint64(minimum)
	if required > ^uint64(0)-headroom || usage.Free <= required+headroom {
		return fmt.Errorf(
			"insufficient disk space for website backup: available %d bytes, reserved %d bytes",
			usage.Free, minimum,
		)
	}
	return nil
}

func scaleProgress(value, start, end int) int {
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}
	return start + (end-start)*value/100
}

func (m *Manager) report(taskID string, progress int, message string) {
	if progress < 1 {
		progress = 1
	}
	if progress > 99 {
		progress = 99
	}
	now := time.Now().UTC()
	_ = m.db.Model(&models.WebsiteTask{}).
		Where("id = ? AND status IN ?", taskID, models.ActiveWebsiteTaskStatuses()).
		Updates(map[string]any{
			"progress": progress, "message": strings.TrimSpace(message), "heartbeat_at": now,
		}).Error
}

func (m *Manager) finish(taskID, status, code, message string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status": status, "progress": 100, "message": message,
		"error_code": code, "error_message": "", "finished_at": now, "heartbeat_at": now,
	}
	if status != models.WebsiteTaskStatusSucceeded {
		updates["error_message"] = message
	}
	return m.db.Model(&models.WebsiteTask{}).Where("id = ?", taskID).Updates(updates).Error
}

func (m *Manager) acquireLocks(task *models.WebsiteTask) error {
	now := time.Now().UTC()
	return m.db.Transaction(func(tx *gorm.DB) error {
		lock := &models.WebsiteOperationLock{
			WebsiteID: task.WebsiteID, TaskID: task.ID,
			AcquiredAt: now, HeartbeatAt: now,
		}
		if err := tx.Create(lock).Error; err != nil {
			return fmt.Errorf("website already has an operation lock: %w", err)
		}
		if task.DatabaseID > 0 {
			databaseLock := &models.DatabaseOperationLock{
				LibraryID: task.DatabaseID, TaskID: task.ID,
				AcquiredAt: now, HeartbeatAt: now,
			}
			if err := tx.Create(databaseLock).Error; err != nil {
				return fmt.Errorf("database already has an operation lock: %w", err)
			}
		}
		return nil
	})
}

func (m *Manager) releaseLocks(task *models.WebsiteTask) {
	_ = m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("website_id = ? AND task_id = ?", task.WebsiteID, task.ID).
			Delete(&models.WebsiteOperationLock{}).Error; err != nil {
			return err
		}
		if task.DatabaseID > 0 {
			return tx.Where("library_id = ? AND task_id = ?", task.DatabaseID, task.ID).
				Delete(&models.DatabaseOperationLock{}).Error
		}
		return nil
	})
}

func (m *Manager) heartbeat(taskID string, websiteID, databaseID int64, done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			now = now.UTC()
			_ = m.db.Model(&models.WebsiteTask{}).Where("id = ?", taskID).
				Update("heartbeat_at", now).Error
			_ = m.db.Model(&models.WebsiteOperationLock{}).
				Where("website_id = ? AND task_id = ?", websiteID, taskID).
				Update("heartbeat_at", now).Error
			if databaseID > 0 {
				_ = m.db.Model(&models.DatabaseOperationLock{}).
					Where("library_id = ? AND task_id = ?", databaseID, taskID).
					Update("heartbeat_at", now).Error
			}
		}
	}
}

func (m *Manager) reconcileInterruptedTasks() error {
	now := time.Now().UTC()
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.WebsiteTask{}).
			Where("status IN ?", models.ActiveWebsiteTaskStatuses()).
			Updates(map[string]any{
				"status": models.WebsiteTaskStatusInterrupted, "progress": 100,
				"message":       "Panel 重启，网站任务已中断",
				"error_code":    "PANEL_RESTARTED",
				"error_message": "Panel 重启，网站任务已中断",
				"finished_at":   now, "heartbeat_at": now,
			}).Error; err != nil {
			return err
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&models.WebsiteOperationLock{}).Error; err != nil {
			return err
		}
		return tx.Where("task_id IN (?)",
			tx.Model(&models.WebsiteTask{}).Select("id")).
			Delete(&models.DatabaseOperationLock{}).Error
	})
}

func (m *Manager) artifactPath(websiteID int64, backupID string) (string, error) {
	if websiteID <= 0 {
		return "", errors.New("invalid website id")
	}
	if _, err := uuid.Parse(backupID); err != nil {
		return "", errors.New("invalid website backup id")
	}
	directory := filepath.Join(m.backupRoot, strconv.FormatInt(websiteID, 10))
	if err := os.MkdirAll(directory, 0750); err != nil {
		return "", err
	}
	if err := ensureRealDirectory(directory); err != nil {
		return "", err
	}
	return filepath.Join(directory, backupID+".tar.gz"), nil
}

func ensureRealDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("path must be a real directory")
	}
	return nil
}

func (m *Manager) safeBackupPath(backup *models.WebsiteBackup) (string, error) {
	expected, err := m.artifactPath(backup.WebsiteID, backup.ID)
	if err != nil {
		return "", err
	}
	absoluteExpected, err := filepath.Abs(expected)
	if err != nil {
		return "", err
	}
	absoluteStored, err := filepath.Abs(filepath.Clean(backup.FilePath))
	if err != nil {
		return "", err
	}
	if absoluteExpected != absoluteStored {
		return "", errors.New("website backup path does not match metadata")
	}
	return absoluteExpected, nil
}

func (m *Manager) verifiedBackup(backupID string) (*models.WebsiteBackup, string, error) {
	var backup models.WebsiteBackup
	if err := m.db.First(&backup, "id = ?", strings.TrimSpace(backupID)).Error; err != nil {
		return nil, "", err
	}
	path, err := m.safeBackupPath(&backup)
	if err != nil {
		return nil, "", err
	}
	size, checksum, err := verifyRegularFile(path)
	if err != nil {
		return nil, "", err
	}
	if size != backup.SizeBytes || !strings.EqualFold(checksum, backup.SHA256) {
		return nil, "", errors.New("website backup integrity check failed")
	}
	return &backup, path, nil
}

func openTaskLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

func (m *Manager) cancelRequested(taskID string) bool {
	var task models.WebsiteTask
	if err := m.db.Select("cancel_requested").First(&task, "id = ?", taskID).Error; err != nil {
		return false
	}
	return task.CancelRequested
}

func (m *Manager) Stop(ctx context.Context) error {
	if !m.stopping.CompareAndSwap(false, true) {
		return nil
	}
	now := time.Now().UTC()
	if err := m.db.Model(&models.WebsiteTask{}).
		Where("status = ?", models.WebsiteTaskStatusQueued).
		Updates(map[string]any{
			"status": models.WebsiteTaskStatusInterrupted, "progress": 100,
			"message":    "Panel 停止，排队任务已中断",
			"error_code": "PANEL_STOPPED", "error_message": "Panel 停止，排队任务已中断",
			"finished_at": now, "heartbeat_at": now,
		}).Error; err != nil {
		return err
	}
	m.stopOnce.Do(func() { close(m.stopCh) })
	m.cancelMu.Lock()
	for _, cancel := range m.cancels {
		cancel()
	}
	m.cancelMu.Unlock()
	done := make(chan struct{})
	go func() {
		m.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
