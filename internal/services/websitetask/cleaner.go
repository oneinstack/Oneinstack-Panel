package websitetask

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
		return nil, errors.New("website backup cleaner manager is not configured")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("website backup retention must be between 1 and 3650 days")
	}
	if strings.TrimSpace(schedule) == "" {
		return nil, errors.New("website backup cleanup schedule is empty")
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
			log.Printf("website backup cleanup failed: %v", cleanupErr)
			return
		}
		if result.BackupsDeleted > 0 {
			log.Printf("website backup cleanup removed %d backups", result.BackupsDeleted)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid website backup cleanup schedule: %w", err)
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
	var backups []models.WebsiteBackup
	if err := c.manager.db.Where("created_at < ?", cutoff).
		Order("created_at ASC").Find(&backups).Error; err != nil {
		return nil, err
	}
	result := &CleanupResult{}
	for i := range backups {
		backup := &backups[i]
		var active int64
		if err := c.manager.db.Model(&models.WebsiteTask{}).
			Where("source_backup_id = ? AND status IN ?", backup.ID, models.ActiveWebsiteTaskStatuses()).
			Count(&active).Error; err != nil {
			return nil, err
		}
		if active > 0 {
			continue
		}
		path, err := c.manager.safeBackupPath(backup)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, err
		case !info.Mode().IsRegular():
			return nil, errors.New("expired website backup is not a regular file")
		default:
			if err := os.Remove(path); err != nil {
				return nil, err
			}
			result.FilesDeleted++
		}
		if err := c.manager.db.Delete(&models.WebsiteBackup{}, "id = ?", backup.ID).Error; err != nil {
			return nil, err
		}
		result.BackupsDeleted++
	}
	return result, nil
}
