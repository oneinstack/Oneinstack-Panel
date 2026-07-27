package audit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type Cleaner struct {
	manager       *Manager
	retentionDays int
	scheduler     *cron.Cron
	now           func() time.Time
	startOnce     sync.Once
	stopOnce      sync.Once
}

func NewCleaner(manager *Manager, retentionDays int, schedule string) (*Cleaner, error) {
	if manager == nil {
		return nil, errors.New("audit cleaner manager is not configured")
	}
	if retentionDays < 30 || retentionDays > 3650 {
		return nil, errors.New("audit retention must be between 30 and 3650 days")
	}
	if strings.TrimSpace(schedule) == "" {
		return nil, errors.New("audit cleanup schedule is empty")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	cleaner := &Cleaner{
		manager:       manager,
		retentionDays: retentionDays,
		scheduler:     scheduler,
		now:           time.Now,
	}
	if _, err := scheduler.AddFunc(schedule, func() {
		result, cleanupErr := cleaner.RunNow()
		if cleanupErr != nil {
			log.Printf("audit cleanup failed: %v", cleanupErr)
			return
		}
		if result.DeletedEntries > 0 {
			log.Printf("audit cleanup removed %d expired entries through sequence %d",
				result.DeletedEntries, result.CheckpointSequence)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid audit cleanup schedule: %w", err)
	}
	return cleaner, nil
}

func (cleaner *Cleaner) Start() {
	if cleaner == nil {
		return
	}
	cleaner.startOnce.Do(func() {
		cleaner.scheduler.Start()
	})
}

func (cleaner *Cleaner) Stop(ctx context.Context) error {
	if cleaner == nil {
		return nil
	}
	var stopped context.Context
	cleaner.stopOnce.Do(func() {
		stopped = cleaner.scheduler.Stop()
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

func (cleaner *Cleaner) RunNow() (*CleanupResult, error) {
	if cleaner == nil || cleaner.manager == nil {
		return nil, errors.New("audit cleaner is not configured")
	}
	cutoff := cleaner.now().UTC().AddDate(0, 0, -cleaner.retentionDays)
	result, err := cleaner.manager.CleanupBefore(cutoff)
	if err != nil {
		return nil, err
	}
	if result.DeletedEntries > 0 {
		_, err = cleaner.manager.Append(EventInput{
			RequestID: NewRequestID(), EventType: "system", Action: "audit.retention_cleanup",
			Status: 200, Outcome: "success", Sensitive: true,
			Message:   fmt.Sprintf("removed %d expired audit entries", result.DeletedEntries),
			CreatedAt: cleaner.now().UTC(),
		})
		if err != nil {
			return nil, fmt.Errorf("record audit cleanup: %w", err)
		}
	}
	return result, nil
}
