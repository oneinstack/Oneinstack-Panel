package app

import (
	"errors"
	"fmt"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

// ErrDatabaseMigration identifies a startup failure caused by the database
// schema or data migration. The CLI maps it to EX_CONFIG so systemd can stop
// retrying a permanently invalid database instead of entering a restart loop.
var ErrDatabaseMigration = errors.New("database migration failed")

const (
	fileArchiveTaskMigrationVersion = "file_archive_task_file_root_path_v1"
	fileExtractTaskMigrationVersion = "file_archive_task_extract_v2"
)

func wrapDatabaseMigration(stage string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%w: %s: %v", ErrDatabaseMigration, stage, err)
}

// migrateFileArchiveTasks performs the schema and legacy-data part of the
// file archive task migration. The operation is safe to run repeatedly:
// SQLite schema inspection is performed before ALTER TABLE and the data
// migration is recorded only after all backfills succeed.
func migrateFileArchiveTasks() error {
	if err := db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
	version TEXT PRIMARY KEY,
	applied_at DATETIME NOT NULL
)`).Error; err != nil {
		return wrapDatabaseMigration("create migration ledger", err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if !tx.Migrator().HasTable(&models.FileArchiveTask{}) {
			if err := tx.AutoMigrate(&models.FileArchiveTask{}); err != nil {
				return wrapDatabaseMigration("create file archive task table", err)
			}
		} else if !tx.Migrator().HasColumn(&models.FileArchiveTask{}, "file_root_path") {
			// SQLite rejects ADD COLUMN ... NOT NULL without a non-NULL
			// default when the table already contains rows.
			if err := tx.Exec(
				"ALTER TABLE `file_archive_task` ADD COLUMN `file_root_path` TEXT NOT NULL DEFAULT ''",
			).Error; err != nil {
				return wrapDatabaseMigration("add file_archive_task.file_root_path", err)
			}
		}
		if err := tx.AutoMigrate(&models.FileArchiveTask{}); err != nil {
			return wrapDatabaseMigration("extend file archive task table", err)
		}

		var applied int64
		if err := tx.Table("schema_migrations").Where("version = ?", fileArchiveTaskMigrationVersion).Count(&applied).Error; err != nil {
			return wrapDatabaseMigration("inspect file archive task migration", err)
		}
		now := time.Now().UTC()
		if applied == 0 {
			if err := tx.Model(&models.FileArchiveTask{}).
				Where("file_root_path IS NULL").
				Update("file_root_path", "").Error; err != nil {
				return wrapDatabaseMigration("backfill file archive task root path", err)
			}

			// A legacy queued/running task has no trustworthy root snapshot. Do
			// not execute it against the current configured root after restart.
			if err := tx.Model(&models.FileArchiveTask{}).
				Where("file_root_path = '' AND status IN ?", []string{
					models.FileArchiveTaskStatusQueued,
					models.FileArchiveTaskStatusRunning,
				}).Updates(map[string]any{
				"status":      models.FileArchiveTaskStatusFailed,
				"message":     "升级后无法恢复旧归档任务，请重新提交",
				"error_code":  "FILE_ARCHIVE_LEGACY_ROOT_UNAVAILABLE",
				"finished_at": now,
				"updated_at":  now,
			}).Error; err != nil {
				return wrapDatabaseMigration("close legacy file archive tasks", err)
			}

			if err := tx.Exec(
				"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
				fileArchiveTaskMigrationVersion, now,
			).Error; err != nil {
				return wrapDatabaseMigration("record file archive task migration", err)
			}
		}

		applied = 0
		if err := tx.Table("schema_migrations").Where("version = ?", fileExtractTaskMigrationVersion).Count(&applied).Error; err != nil {
			return wrapDatabaseMigration("inspect file extract task migration", err)
		}
		if applied == 0 {
			if err := tx.Model(&models.FileArchiveTask{}).
				Where("operation IS NULL OR operation = ''").
				Update("operation", models.FileArchiveTaskOperationArchive).Error; err != nil {
				return wrapDatabaseMigration("backfill file task operation", err)
			}
			if err := tx.Exec(
				"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
				fileExtractTaskMigrationVersion, now,
			).Error; err != nil {
				return wrapDatabaseMigration("record file extract task migration", err)
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return nil
}
