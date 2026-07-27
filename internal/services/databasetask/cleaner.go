package databasetask

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"

	"github.com/robfig/cron/v3"
)

type CleanupResult struct {
	BackupsDeleted int `json:"backupsDeleted"`
	FilesDeleted   int `json:"filesDeleted"`
}

type Cleaner struct {
	manager       *Manager
	retentionDays int
	scheduler     *cron.Cron
	now           func() time.Time
	startOnce     sync.Once
	stopOnce      sync.Once
}

func NewCleaner(manager *Manager, retentionDays int, schedule string) (*Cleaner, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("database backup cleaner manager is not configured")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("database backup retention must be between 1 and 3650 days")
	}
	if strings.TrimSpace(schedule) == "" {
		return nil, errors.New("database backup cleanup schedule is empty")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	cleaner := &Cleaner{
		manager: manager, retentionDays: retentionDays,
		scheduler: scheduler, now: time.Now,
	}
	if _, err := scheduler.AddFunc(schedule, func() {
		result, cleanupErr := cleaner.RunNow()
		if cleanupErr != nil {
			log.Printf("database backup cleanup failed: %v", cleanupErr)
			return
		}
		if result.BackupsDeleted > 0 {
			log.Printf("database backup cleanup removed %d backups", result.BackupsDeleted)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid database backup cleanup schedule: %w", err)
	}
	return cleaner, nil
}

func (c *Cleaner) Start() {
	if c != nil {
		c.startOnce.Do(func() { c.scheduler.Start() })
	}
}

func (c *Cleaner) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var stopped context.Context
	c.stopOnce.Do(func() { stopped = c.scheduler.Stop() })
	if stopped == nil {
		return nil
	}
	select {
	case <-stopped.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Cleaner) RunNow() (*CleanupResult, error) {
	if err := c.manager.Start(); err != nil {
		return nil, err
	}
	cutoff := c.now().UTC().AddDate(0, 0, -c.retentionDays)
	var backups []models.DatabaseBackup
	if err := c.manager.db.Where("created_at < ?", cutoff).
		Order("created_at ASC").Find(&backups).Error; err != nil {
		return nil, err
	}
	result := &CleanupResult{}
	for i := range backups {
		backup := &backups[i]
		var active int64
		if err := c.manager.db.Model(&models.DatabaseTask{}).
			Where("source_backup_id = ? AND status IN ?", backup.ID, models.ActiveDatabaseTaskStatuses()).
			Count(&active).Error; err != nil {
			return nil, err
		}
		if active > 0 {
			continue
		}
		path, err := c.manager.safeBackupPath(backup)
		if err != nil {
			return nil, fmt.Errorf("validate expired backup %s: %w", backup.ID, err)
		}
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, err
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("expired database backup %s is not a regular file", backup.ID)
		default:
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			result.FilesDeleted++
		}
		if err := c.manager.db.Delete(&models.DatabaseBackup{}, "id = ?", backup.ID).Error; err != nil {
			return nil, err
		}
		result.BackupsDeleted++
	}
	return result, nil
}
