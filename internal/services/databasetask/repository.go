package databasetask

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type ListOptions struct {
	LibraryID int64
	Operation string
	Status    string
	Page      int
	PageSize  int
}

type TaskList struct {
	Data     []models.DatabaseTask `json:"data"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type BackupList struct {
	Data     []models.DatabaseBackup `json:"data"`
	Total    int64                   `json:"total"`
	Page     int                     `json:"page"`
	PageSize int                     `json:"pageSize"`
}

type LogChunk struct {
	Content    string `json:"content"`
	NextCursor int64  `json:"nextCursor"`
	EOF        bool   `json:"eof"`
}

func (m *Manager) GetTask(taskID string) (*models.DatabaseTask, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	var task models.DatabaseTask
	if err := m.db.First(&task, "id = ?", strings.TrimSpace(taskID)).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *Manager) ListTasks(options ListOptions) (*TaskList, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	options.Page, options.PageSize = normalizePage(options.Page, options.PageSize)
	query := m.db.Model(&models.DatabaseTask{})
	if options.LibraryID > 0 {
		query = query.Where("library_id = ?", options.LibraryID)
	}
	if value := strings.TrimSpace(options.Operation); value != "" {
		query = query.Where("operation = ?", value)
	}
	if value := strings.TrimSpace(options.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.DatabaseTask
	if err := query.Order("created_at DESC").
		Offset((options.Page - 1) * options.PageSize).
		Limit(options.PageSize).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return &TaskList{
		Data: tasks, Total: total,
		Page: options.Page, PageSize: options.PageSize,
	}, nil
}

func (m *Manager) ListBackups(libraryID int64, page, pageSize int) (*BackupList, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	page, pageSize = normalizePage(page, pageSize)
	query := m.db.Model(&models.DatabaseBackup{})
	if libraryID > 0 {
		query = query.Where("library_id = ?", libraryID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var backups []models.DatabaseBackup
	if err := query.Order("created_at DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&backups).Error; err != nil {
		return nil, err
	}
	return &BackupList{
		Data: backups, Total: total,
		Page: page, PageSize: pageSize,
	}, nil
}

func (m *Manager) GetBackup(backupID string) (*models.DatabaseBackup, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	var backup models.DatabaseBackup
	if err := m.db.First(&backup, "id = ?", strings.TrimSpace(backupID)).Error; err != nil {
		return nil, err
	}
	return &backup, nil
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func (m *Manager) Cancel(taskID string) (*models.DatabaseTask, error) {
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if models.IsDatabaseTaskTerminal(task.Status) {
		return task, nil
	}
	if task.Status == models.DatabaseTaskStatusQueued {
		if err := m.finish(
			task.ID,
			models.DatabaseTaskStatusCanceled,
			"TASK_CANCELED",
			"数据库任务已取消",
		); err != nil {
			return nil, err
		}
	} else {
		if err := m.db.Model(&models.DatabaseTask{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status":           models.DatabaseTaskStatusCanceling,
			"cancel_requested": true,
			"message":          "正在取消数据库任务",
		}).Error; err != nil {
			return nil, err
		}
		m.cancelMu.Lock()
		cancel := m.cancels[task.ID]
		m.cancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return m.GetTask(task.ID)
}

func (m *Manager) OpenBackup(
	backupID string,
) (*os.File, os.FileInfo, *models.DatabaseBackup, error) {
	backup, path, err := m.verifiedBackup(strings.TrimSpace(backupID))
	if err != nil {
		return nil, nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, nil, err
	}
	return file, info, backup, nil
}

func (m *Manager) DeleteBackup(backupID string) error {
	backup, err := m.GetBackup(strings.TrimSpace(backupID))
	if err != nil {
		return err
	}
	path, err := m.safeBackupPath(backup)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("database backup is not a regular file")
	}
	var active int64
	if err := m.db.Model(&models.DatabaseTask{}).
		Where("source_backup_id = ? AND status IN ?", backup.ID, models.ActiveDatabaseTaskStatuses()).
		Count(&active).Error; err != nil {
		return err
	}
	if active > 0 {
		return errors.New("backup is currently used by an active restore task")
	}
	tombstone := path + ".deleting"
	if err := os.Rename(path, tombstone); err != nil {
		return fmt.Errorf("prepare database backup deletion: %w", err)
	}
	if err := m.db.Delete(&models.DatabaseBackup{}, "id = ?", backup.ID).Error; err != nil {
		_ = os.Rename(tombstone, path)
		return err
	}
	if err := os.Remove(tombstone); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete database backup file: %w", err)
	}
	return nil
}

func (m *Manager) ReadLog(taskID string, cursor, limit int64) (*LogChunk, error) {
	if cursor < 0 {
		return nil, errors.New("log cursor cannot be negative")
	}
	if limit <= 0 || limit > 64*1024 {
		limit = 64 * 1024
	}
	task, err := m.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(task.LogPath) == "" {
		return &LogChunk{NextCursor: cursor, EOF: models.IsDatabaseTaskTerminal(task.Status)}, nil
	}
	expected := filepath.Join(m.logRoot, "task_"+task.ID+".log")
	absoluteExpected, _ := filepath.Abs(expected)
	absoluteStored, _ := filepath.Abs(filepath.Clean(task.LogPath))
	if absoluteExpected != absoluteStored {
		return nil, errors.New("database task log path does not match task")
	}
	info, err := os.Lstat(absoluteStored)
	if errors.Is(err, os.ErrNotExist) {
		return &LogChunk{NextCursor: cursor, EOF: models.IsDatabaseTaskTerminal(task.Status)}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("database task log is not a regular file")
	}
	file, err := os.Open(absoluteStored)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if cursor > info.Size() {
		return nil, errors.New("log cursor exceeds current log size")
	}
	buffer := make([]byte, limit)
	read, readErr := file.ReadAt(buffer, cursor)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	next := cursor + int64(read)
	return &LogChunk{
		Content: string(buffer[:read]), NextCursor: next,
		EOF: next >= info.Size() && models.IsDatabaseTaskTerminal(task.Status),
	}, nil
}

func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound) || errors.Is(err, os.ErrNotExist)
}
