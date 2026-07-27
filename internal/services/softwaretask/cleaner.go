package softwaretask

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
	"gorm.io/gorm"
)

type CleanupResult struct {
	LogFilesDeleted int `json:"logFilesDeleted"`
	EventsDeleted   int `json:"eventsDeleted"`
	TasksDeleted    int `json:"tasksDeleted"`
}

type Cleaner struct {
	manager           *Manager
	taskRetentionDays int
	logRetentionDays  int
	scheduler         *cron.Cron
	now               func() time.Time
	startOnce         sync.Once
	stopOnce          sync.Once
}

func NewCleaner(manager *Manager, taskRetentionDays, logRetentionDays int, schedule string) (*Cleaner, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("software task cleaner manager is not configured")
	}
	if taskRetentionDays < 1 || taskRetentionDays > 3650 {
		return nil, errors.New("software task retention must be between 1 and 3650 days")
	}
	if logRetentionDays < 1 || logRetentionDays > taskRetentionDays {
		return nil, errors.New("software task log retention must be between 1 and task retention days")
	}
	if strings.TrimSpace(schedule) == "" {
		return nil, errors.New("software task cleanup schedule is empty")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	cleaner := &Cleaner{
		manager:           manager,
		taskRetentionDays: taskRetentionDays,
		logRetentionDays:  logRetentionDays,
		scheduler:         scheduler,
		now:               time.Now,
	}
	if _, err := scheduler.AddFunc(schedule, func() {
		result, cleanupErr := cleaner.RunNow()
		if cleanupErr != nil {
			log.Printf("software task cleanup failed: %v", cleanupErr)
			return
		}
		if result.LogFilesDeleted+result.EventsDeleted+result.TasksDeleted > 0 {
			log.Printf(
				"software task cleanup removed %d logs, %d events and %d task summaries",
				result.LogFilesDeleted,
				result.EventsDeleted,
				result.TasksDeleted,
			)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid software task cleanup schedule: %w", err)
	}
	return cleaner, nil
}

func (c *Cleaner) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		c.scheduler.Start()
	})
}

func (c *Cleaner) Stop(ctx context.Context) error {
	if c == nil {
		return nil
	}
	var stopped context.Context
	c.stopOnce.Do(func() {
		stopped = c.scheduler.Stop()
	})
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
	if c == nil || c.manager == nil {
		return nil, errors.New("software task cleaner is not configured")
	}
	if err := c.manager.Start(); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	logCutoff := now.AddDate(0, 0, -c.logRetentionDays)
	taskCutoff := now.AddDate(0, 0, -c.taskRetentionDays)
	result := &CleanupResult{}

	var expiredLogs []models.SoftwareTask
	if err := c.manager.db.
		Select("id", "log_path").
		Where("status IN ? AND COALESCE(finished_at, updated_at) < ?", terminalStatuses(), logCutoff).
		Find(&expiredLogs).Error; err != nil {
		return nil, fmt.Errorf("list expired software task logs: %w", err)
	}
	for i := range expiredLogs {
		task := &expiredLogs[i]
		if strings.TrimSpace(task.LogPath) == "" {
			continue
		}
		path, err := c.manager.safeLogPath(task.ID, task.LogPath)
		if err != nil {
			return nil, fmt.Errorf("validate expired log for task %s: %w", task.ID, err)
		}
		info, err := os.Lstat(path)
		switch {
		case errors.Is(err, os.ErrNotExist):
		case err != nil:
			return nil, fmt.Errorf("stat expired log for task %s: %w", task.ID, err)
		case !info.Mode().IsRegular():
			return nil, fmt.Errorf("expired log for task %s is not a regular file", task.ID)
		default:
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("delete expired log for task %s: %w", task.ID, err)
			}
			result.LogFilesDeleted++
		}
		if err := c.manager.db.Model(&models.SoftwareTask{}).
			Where("id = ? AND log_path = ?", task.ID, task.LogPath).
			Update("log_path", "").Error; err != nil {
			return nil, fmt.Errorf("clear expired log path for task %s: %w", task.ID, err)
		}
	}

	if err := c.manager.db.Transaction(func(tx *gorm.DB) error {
		var expiredTaskIDs []string
		if err := tx.Model(&models.SoftwareTask{}).
			Where("status IN ? AND COALESCE(finished_at, updated_at) < ?", terminalStatuses(), taskCutoff).
			Pluck("id", &expiredTaskIDs).Error; err != nil {
			return err
		}
		if len(expiredTaskIDs) > 0 {
			deletedEvents := tx.Where("task_id IN ?", expiredTaskIDs).Delete(&models.SoftwareTaskEvent{})
			if deletedEvents.Error != nil {
				return deletedEvents.Error
			}
			result.EventsDeleted += int(deletedEvents.RowsAffected)
			deletedTasks := tx.Where("id IN ?", expiredTaskIDs).Delete(&models.SoftwareTask{})
			if deletedTasks.Error != nil {
				return deletedTasks.Error
			}
			result.TasksDeleted += int(deletedTasks.RowsAffected)
		}

		var eventOnlyTaskIDs []string
		if err := tx.Model(&models.SoftwareTask{}).
			Where(
				"status IN ? AND COALESCE(finished_at, updated_at) < ? AND COALESCE(finished_at, updated_at) >= ?",
				terminalStatuses(),
				logCutoff,
				taskCutoff,
			).
			Pluck("id", &eventOnlyTaskIDs).Error; err != nil {
			return err
		}
		if len(eventOnlyTaskIDs) > 0 {
			deletedEvents := tx.Where("task_id IN ?", eventOnlyTaskIDs).Delete(&models.SoftwareTaskEvent{})
			if deletedEvents.Error != nil {
				return deletedEvents.Error
			}
			result.EventsDeleted += int(deletedEvents.RowsAffected)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("delete expired software task history: %w", err)
	}
	return result, nil
}

func terminalStatuses() []string {
	return []string{
		models.SoftwareTaskStatusSucceeded,
		models.SoftwareTaskStatusFailed,
		models.SoftwareTaskStatusCanceled,
		models.SoftwareTaskStatusInterrupted,
	}
}
