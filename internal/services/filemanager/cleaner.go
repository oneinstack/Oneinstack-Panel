package filemanager

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

type TrashCleaner struct {
	rootPath      string
	retentionDays int
	scheduler     *cron.Cron
	startOnce     sync.Once
	stopOnce      sync.Once
}

func NewTrashCleaner(rootPath string, retentionDays int, schedule string) (*TrashCleaner, error) {
	rootPath = strings.TrimSpace(rootPath)
	if rootPath == "" {
		return nil, fmt.Errorf("%w: file root is empty", ErrInvalidPath)
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, fmt.Errorf("%w: trash retention must be between 1 and 3650 days", ErrInvalidPath)
	}
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return nil, fmt.Errorf("%w: trash cleanup schedule is empty", ErrInvalidPath)
	}

	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	cleaner := &TrashCleaner{
		rootPath:      rootPath,
		retentionDays: retentionDays,
		scheduler:     scheduler,
	}
	if _, err := scheduler.AddFunc(schedule, func() {
		deleted, cleanupErr := cleaner.RunNow()
		if cleanupErr != nil {
			log.Printf("trash cleanup failed: %v", cleanupErr)
			return
		}
		if deleted > 0 {
			log.Printf("trash cleanup permanently deleted %d expired entries", deleted)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid trash cleanup schedule: %w", err)
	}
	return cleaner, nil
}

func (cleaner *TrashCleaner) Start() {
	if cleaner == nil {
		return
	}
	cleaner.startOnce.Do(func() {
		cleaner.scheduler.Start()
	})
}

func (cleaner *TrashCleaner) RunNow() (int, error) {
	if cleaner == nil {
		return 0, errorsNilCleaner()
	}
	manager, err := New(cleaner.rootPath)
	if err != nil {
		return 0, err
	}
	defer manager.Close()
	cutoff := time.Now().UTC().AddDate(0, 0, -cleaner.retentionDays)
	return manager.CleanupTrashBefore(cutoff)
}

func (cleaner *TrashCleaner) Stop(ctx context.Context) error {
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

func errorsNilCleaner() error {
	return fmt.Errorf("%w: trash cleaner is nil", ErrInvalidPath)
}
