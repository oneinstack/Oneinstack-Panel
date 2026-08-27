package databasetask

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultQueueSize                    = 64
	defaultWorkerSize                   = 2
	databaseConnectionValidationTimeout = 10 * time.Second
)

type ProgressReporter func(progress int, message string)

// Operator performs database-specific work. Implementations must honor ctx,
// write diagnostic output without secrets, and atomically publish destination.
type Operator interface {
	Backup(
		ctx context.Context,
		libraryID int64,
		destination string,
		log io.Writer,
		report ProgressReporter,
	) error
	Restore(
		ctx context.Context,
		libraryID int64,
		source string,
		log io.Writer,
		report ProgressReporter,
	) error
}

type connectionValidator interface {
	ValidateConnection(ctx context.Context, libraryID int64) error
}

type Request struct {
	Operation string
	LibraryID int64
	BackupID  string
}

type queuedTask struct {
	taskID  string
	request Request
}

type Manager struct {
	db         *gorm.DB
	backupRoot string
	logRoot    string
	operator   Operator
	queue      chan queuedTask
	stopCh     chan struct{}

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	submitMu  sync.Mutex
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc
	runWG     sync.WaitGroup
	stopping  atomic.Bool
}

func NewManager(db *gorm.DB, backupRoot, logRoot string, operator Operator) *Manager {
	return &Manager{
		db:         db,
		backupRoot: filepath.Clean(backupRoot),
		logRoot:    filepath.Clean(logRoot),
		operator:   operator,
		queue:      make(chan queuedTask, defaultQueueSize),
		stopCh:     make(chan struct{}),
		cancels:    make(map[string]context.CancelFunc),
	}
}

func (m *Manager) Start() error {
	m.startOnce.Do(func() {
		switch {
		case m.db == nil:
			m.startErr = errors.New("database task database is not initialized")
		case m.operator == nil:
			m.startErr = errors.New("database task operator is not configured")
		case invalidRoot(m.backupRoot):
			m.startErr = errors.New("database backup directory is invalid")
		case invalidRoot(m.logRoot):
			m.startErr = errors.New("database task log directory is invalid")
		}
		if m.startErr != nil {
			return
		}
		if err := os.MkdirAll(m.backupRoot, 0750); err != nil {
			m.startErr = fmt.Errorf("create database backup directory: %w", err)
			return
		}
		if err := os.MkdirAll(m.logRoot, 0750); err != nil {
			m.startErr = fmt.Errorf("create database task log directory: %w", err)
			return
		}
		if err := m.reconcileInterruptedTasks(); err != nil {
			m.startErr = err
			return
		}
		for i := 0; i < defaultWorkerSize; i++ {
			go m.worker()
		}
	})
	return m.startErr
}

func invalidRoot(path string) bool {
	return path == "" || path == "." || path == string(filepath.Separator)
}

func (m *Manager) SubmitBackup(libraryID, requestedBy int64) (*models.DatabaseTask, error) {
	return m.submit(Request{Operation: "backup", LibraryID: libraryID}, requestedBy)
}

func (m *Manager) SubmitRestore(
	libraryID int64,
	backupID string,
	requestedBy int64,
) (*models.DatabaseTask, error) {
	return m.submit(Request{
		Operation: "restore",
		LibraryID: libraryID,
		BackupID:  strings.TrimSpace(backupID),
	}, requestedBy)
}

func (m *Manager) submit(request Request, requestedBy int64) (*models.DatabaseTask, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	if m.stopping.Load() {
		return nil, errors.New("database task manager is stopping")
	}
	if request.LibraryID <= 0 || requestedBy <= 0 {
		return nil, errors.New("database and authenticated user are required")
	}
	if request.Operation != "backup" && request.Operation != "restore" {
		return nil, fmt.Errorf("unsupported database operation: %s", request.Operation)
	}

	var library models.Library
	if err := m.db.First(&library, request.LibraryID).Error; err != nil {
		return nil, err
	}
	if library.Type != "mysql" {
		return nil, errors.New("database backup and restore currently support MySQL only")
	}
	if request.Operation == "restore" {
		if request.BackupID == "" {
			return nil, errors.New("source backup is required")
		}
		var backup models.DatabaseBackup
		if err := m.db.First(&backup, "id = ?", request.BackupID).Error; err != nil {
			return nil, err
		}
		if backup.LibraryID != library.ID || backup.DatabaseName != library.Name {
			return nil, errors.New("backup does not belong to the selected database")
		}
	}
	if validator, ok := m.operator.(connectionValidator); ok {
		validationContext, cancel := context.WithTimeout(context.Background(), databaseConnectionValidationTimeout)
		err := validator.ValidateConnection(validationContext, request.LibraryID)
		cancel()
		if err != nil {
			return nil, err
		}
	}

	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	var active int64
	if err := m.db.Model(&models.DatabaseTask{}).
		Where("library_id = ? AND status IN ?", library.ID, models.ActiveDatabaseTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, fmt.Errorf("check active database task: %w", err)
	}
	if active > 0 {
		return nil, fmt.Errorf("database %s already has an active task", library.Name)
	}

	taskID := uuid.NewString()
	now := time.Now().UTC()
	message := "数据库备份任务已进入队列"
	if request.Operation == "restore" {
		message = "数据库恢复任务已进入队列"
	}
	task := &models.DatabaseTask{
		ID:             taskID,
		Operation:      request.Operation,
		LibraryID:      library.ID,
		DatabaseName:   library.Name,
		SourceBackupID: request.BackupID,
		Status:         models.DatabaseTaskStatusQueued,
		Progress:       0,
		Message:        message,
		RequestedBy:    requestedBy,
		LogPath:        filepath.Join(m.logRoot, "task_"+taskID+".log"),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := m.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create database task: %w", err)
	}

	select {
	case m.queue <- queuedTask{taskID: taskID, request: request}:
		return task, nil
	case <-m.stopCh:
		_ = m.finish(task.ID, models.DatabaseTaskStatusInterrupted, "MANAGER_STOPPED", "任务服务已停止")
		return nil, errors.New("database task manager is stopping")
	default:
		_ = m.finish(task.ID, models.DatabaseTaskStatusFailed, "QUEUE_FULL", "数据库任务队列已满")
		return nil, errors.New("database task queue is full")
	}
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
	var task models.DatabaseTask
	if err := m.db.First(&task, "id = ?", item.taskID).Error; err != nil {
		return
	}
	if models.IsDatabaseTaskTerminal(task.Status) || task.CancelRequested {
		return
	}
	if err := m.acquireLock(task.LibraryID, task.ID); err != nil {
		_ = m.finish(task.ID, models.DatabaseTaskStatusFailed, "DATABASE_BUSY", err.Error())
		return
	}
	defer m.releaseLock(task.LibraryID, task.ID)

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
		_ = m.finish(task.ID, models.DatabaseTaskStatusFailed, "LOG_OPEN_FAILED", err.Error())
		return
	}
	defer logFile.Close()

	now := time.Now().UTC()
	_ = m.db.Model(&models.DatabaseTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":       models.DatabaseTaskStatusRunning,
		"progress":     1,
		"message":      "数据库任务开始执行",
		"started_at":   now,
		"heartbeat_at": now,
	}).Error
	_, _ = fmt.Fprintf(logFile, "[%s] %s task started for database %s\n",
		now.Format(time.RFC3339), task.Operation, task.DatabaseName)

	heartbeatDone := make(chan struct{})
	go m.heartbeat(task.ID, task.LibraryID, heartbeatDone)
	defer close(heartbeatDone)

	report := func(progress int, message string) {
		m.report(task.ID, progress, message)
	}
	if task.Operation == "backup" {
		err = m.runBackup(ctx, &task, logFile, report)
	} else {
		err = m.runRestore(ctx, &task, logFile, report)
	}
	if err != nil {
		status := models.DatabaseTaskStatusFailed
		code := "DATABASE_OPERATION_FAILED"
		message := err.Error()
		if classifiedCode, classifiedMessage := classifyDatabaseTaskError(err); classifiedMessage != "" {
			code = classifiedCode
			message = classifiedMessage
		}
		if errors.Is(err, context.Canceled) || m.cancelRequested(task.ID) {
			if m.stopping.Load() {
				status = models.DatabaseTaskStatusInterrupted
				code = "PANEL_STOPPED"
				message = "Panel 停止，数据库任务已中断"
			} else {
				status = models.DatabaseTaskStatusCanceled
				code = "TASK_CANCELED"
				message = "数据库任务已取消"
			}
		}
		_, _ = fmt.Fprintf(logFile, "[%s] task failed: %s\n", time.Now().UTC().Format(time.RFC3339), err)
		_ = m.finish(task.ID, status, code, message)
		return
	}
	_, _ = fmt.Fprintf(logFile, "[%s] task completed\n", time.Now().UTC().Format(time.RFC3339))
	_ = m.finish(task.ID, models.DatabaseTaskStatusSucceeded, "", "数据库任务执行成功")
}

func classifyDatabaseTaskError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	lower := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return "DATABASE_CONNECTION_TIMEOUT", "目标数据库在 5 秒内未响应，请检查地址、端口、防火墙和数据库服务状态后重试。"
	case strings.Contains(lower, "access denied"),
		strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "wrongpass"),
		strings.Contains(lower, "invalid username-password pair"),
		strings.Contains(lower, "invalid password"):
		return "DATABASE_AUTH_FAILED", "目标数据库已响应，但用户名或密码未通过认证，请核对登录凭据。"
	case strings.Contains(lower, "connection refused"):
		return "DATABASE_CONNECTION_REFUSED", "目标数据库拒绝连接，请确认服务已启动、监听地址和端口配置正确。"
	case strings.Contains(lower, "no such host"):
		return "DATABASE_HOST_UNREACHABLE", "无法解析目标数据库地址，请检查地址和 DNS 配置后重试。"
	case strings.Contains(lower, "network is unreachable"),
		strings.Contains(lower, "no route to host"):
		return "DATABASE_HOST_UNREACHABLE", "面板服务器当前无法访问目标数据库网络，请检查路由、安全组和防火墙配置。"
	case strings.Contains(lower, "connection reset"),
		strings.Contains(lower, "connection closed"),
		strings.Contains(lower, "broken pipe"),
		strings.Contains(lower, "lost connection"),
		strings.Contains(lower, "server has gone away"),
		strings.Contains(lower, "database connection unavailable"),
		strings.Contains(lower, "database connection test failed"):
		return "DATABASE_CONNECTION_FAILED", "无法连接到目标数据库，请检查地址、端口、登录凭据和网络访问策略后重试。"
	case strings.Contains(lower, "mysqldump failed"),
		strings.Contains(lower, "mysql restore failed"):
		return "DATABASE_OPERATION_FAILED", "数据库备份或恢复失败，请查看任务日志中的具体原因后重试。"
	default:
		return "", ""
	}
}

func (m *Manager) runBackup(
	ctx context.Context,
	task *models.DatabaseTask,
	log io.Writer,
	report ProgressReporter,
) error {
	backupID := uuid.NewString()
	destination, err := m.artifactPath(task.LibraryID, backupID)
	if err != nil {
		return err
	}
	report(5, "正在准备数据库备份")
	err = m.operator.Backup(ctx, task.LibraryID, destination, log, func(progress int, message string) {
		report(scaleProgress(progress, 5, 90), message)
	})
	if err != nil {
		removePartialArtifacts(destination)
		return err
	}
	backup, err := m.registerBackup(
		backupID,
		task,
		destination,
		models.DatabaseBackupSourceManual,
	)
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	if err := m.db.Model(&models.DatabaseTask{}).Where("id = ?", task.ID).
		Update("result_backup_id", backup.ID).Error; err != nil {
		return err
	}
	report(98, "备份文件校验完成")
	return nil
}

func (m *Manager) runRestore(
	ctx context.Context,
	task *models.DatabaseTask,
	log io.Writer,
	report ProgressReporter,
) error {
	sourceBackup, sourcePath, err := m.verifiedBackup(task.SourceBackupID)
	if err != nil {
		return fmt.Errorf("verify restore source: %w", err)
	}
	if sourceBackup.LibraryID != task.LibraryID ||
		sourceBackup.DatabaseName != task.DatabaseName {
		return errors.New("restore source does not belong to this database")
	}
	report(3, "恢复源校验完成，正在创建恢复前安全备份")

	safetyID := uuid.NewString()
	safetyPath, err := m.artifactPath(task.LibraryID, safetyID)
	if err != nil {
		return err
	}
	err = m.operator.Backup(ctx, task.LibraryID, safetyPath, log, func(progress int, message string) {
		report(scaleProgress(progress, 3, 42), "安全备份："+message)
	})
	if err != nil {
		removePartialArtifacts(safetyPath)
		return fmt.Errorf("create pre-restore safety backup: %w", err)
	}
	safetyBackup, err := m.registerBackup(
		safetyID,
		task,
		safetyPath,
		models.DatabaseBackupSourcePreRestore,
	)
	if err != nil {
		_ = os.Remove(safetyPath)
		return fmt.Errorf("register pre-restore safety backup: %w", err)
	}
	if err := m.db.Model(&models.DatabaseTask{}).Where("id = ?", task.ID).
		Update("safety_backup_id", safetyBackup.ID).Error; err != nil {
		return err
	}

	report(45, "安全备份已完成，开始恢复数据库")
	err = m.operator.Restore(ctx, task.LibraryID, sourcePath, log, func(progress int, message string) {
		report(scaleProgress(progress, 45, 95), message)
	})
	if err != nil {
		return err
	}
	report(98, "数据库恢复完成")
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
	_ = m.db.Model(&models.DatabaseTask{}).
		Where("id = ? AND status IN ?", taskID, models.ActiveDatabaseTaskStatuses()).
		Updates(map[string]any{
			"progress":     progress,
			"message":      strings.TrimSpace(message),
			"heartbeat_at": now,
		}).Error
}

func (m *Manager) finish(taskID, status, code, message string) error {
	now := time.Now().UTC()
	updates := map[string]any{
		"status":        status,
		"progress":      100,
		"message":       message,
		"error_code":    code,
		"error_message": "",
		"finished_at":   now,
		"heartbeat_at":  now,
	}
	if status != models.DatabaseTaskStatusSucceeded {
		updates["error_message"] = message
	}
	return m.db.Model(&models.DatabaseTask{}).Where("id = ?", taskID).Updates(updates).Error
}

func (m *Manager) heartbeat(taskID string, libraryID int64, done <-chan struct{}) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case now := <-ticker.C:
			_ = m.db.Model(&models.DatabaseTask{}).Where("id = ?", taskID).
				Update("heartbeat_at", now.UTC()).Error
			_ = m.db.Model(&models.DatabaseOperationLock{}).
				Where("library_id = ? AND task_id = ?", libraryID, taskID).
				Update("heartbeat_at", now.UTC()).Error
		}
	}
}

func (m *Manager) acquireLock(libraryID int64, taskID string) error {
	now := time.Now().UTC()
	lock := &models.DatabaseOperationLock{
		LibraryID: libraryID, TaskID: taskID,
		AcquiredAt: now, HeartbeatAt: now,
	}
	if err := m.db.Create(lock).Error; err != nil {
		return fmt.Errorf("database already has an operation lock: %w", err)
	}
	return nil
}

func (m *Manager) releaseLock(libraryID int64, taskID string) {
	_ = m.db.Where("library_id = ? AND task_id = ?", libraryID, taskID).
		Delete(&models.DatabaseOperationLock{}).Error
}

func (m *Manager) reconcileInterruptedTasks() error {
	now := time.Now().UTC()
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.DatabaseTask{}).
			Where("status IN ?", models.ActiveDatabaseTaskStatuses()).
			Updates(map[string]any{
				"status":        models.DatabaseTaskStatusInterrupted,
				"progress":      100,
				"message":       "Panel 重启，数据库任务已中断",
				"error_code":    "PANEL_RESTARTED",
				"error_message": "Panel 重启，数据库任务已中断",
				"finished_at":   now,
				"heartbeat_at":  now,
			}).Error; err != nil {
			return fmt.Errorf("reconcile interrupted database tasks: %w", err)
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&models.DatabaseOperationLock{}).Error; err != nil {
			return fmt.Errorf("clear stale database locks: %w", err)
		}
		return nil
	})
}

func (m *Manager) artifactPath(libraryID int64, backupID string) (string, error) {
	if libraryID <= 0 {
		return "", errors.New("invalid database id")
	}
	if _, err := uuid.Parse(backupID); err != nil {
		return "", errors.New("invalid backup id")
	}
	directory := filepath.Join(m.backupRoot, strconv.FormatInt(libraryID, 10))
	if err := os.MkdirAll(directory, 0750); err != nil {
		return "", fmt.Errorf("create database backup directory: %w", err)
	}
	return filepath.Join(directory, backupID+".sql.gz"), nil
}

func (m *Manager) registerBackup(
	backupID string,
	task *models.DatabaseTask,
	path string,
	source string,
) (*models.DatabaseBackup, error) {
	size, checksum, err := verifyRegularFile(path)
	if err != nil {
		return nil, err
	}
	var library models.Library
	if err := m.db.Select("id", "p_id", "name").First(&library, task.LibraryID).Error; err != nil {
		return nil, err
	}
	backup := &models.DatabaseBackup{
		ID: backupID, LibraryID: task.LibraryID, ConnectionID: library.PID,
		DatabaseName: task.DatabaseName, Source: source,
		FileName: task.DatabaseName + "_" + time.Now().UTC().Format("20060102_150405") + ".sql.gz",
		FilePath: path, SizeBytes: size, SHA256: checksum,
		CreatedBy: task.RequestedBy, CreatedAt: time.Now().UTC(),
	}
	if err := m.db.Create(backup).Error; err != nil {
		return nil, err
	}
	return backup, nil
}

func (m *Manager) verifiedBackup(backupID string) (*models.DatabaseBackup, string, error) {
	var backup models.DatabaseBackup
	if err := m.db.First(&backup, "id = ?", backupID).Error; err != nil {
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
		return nil, "", errors.New("database backup integrity check failed")
	}
	return &backup, path, nil
}

func verifyRegularFile(path string) (int64, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", errors.New("database backup is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if !os.SameFile(info, openedInfo) {
		return 0, "", errors.New("database backup changed while opening")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return openedInfo.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *Manager) safeBackupPath(backup *models.DatabaseBackup) (string, error) {
	expected, err := m.artifactPath(backup.LibraryID, backup.ID)
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
	if absoluteStored != absoluteExpected {
		return "", errors.New("database backup path does not match its metadata")
	}
	return absoluteExpected, nil
}

func openTaskLog(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
}

func removePartialArtifacts(destination string) {
	_ = os.Remove(destination)
	_ = os.Remove(destination + ".partial")
}

func (m *Manager) cancelRequested(taskID string) bool {
	var task models.DatabaseTask
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
	if err := m.db.Model(&models.DatabaseTask{}).
		Where("status = ?", models.DatabaseTaskStatusQueued).
		Updates(map[string]any{
			"status":        models.DatabaseTaskStatusInterrupted,
			"progress":      100,
			"message":       "Panel 停止，排队任务已中断",
			"error_code":    "PANEL_STOPPED",
			"error_message": "Panel 停止，排队任务已中断",
			"finished_at":   now,
			"heartbeat_at":  now,
		}).Error; err != nil {
		return err
	}
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
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
