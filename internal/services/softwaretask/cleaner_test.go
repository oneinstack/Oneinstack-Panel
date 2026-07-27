package softwaretask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oneinstack/internal/models"
)

func TestCleanerAppliesSeparateLogAndSummaryRetention(t *testing.T) {
	db := openTaskTestDB(t)
	logDir := t.TempDir()
	manager := NewManager(db, logDir, func(
		context.Context,
		InstallRequest,
		string,
		*Reporter,
	) error {
		return nil
	})
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	createFinishedTask := func(id string, ageDays int) string {
		t.Helper()
		finished := now.AddDate(0, 0, -ageDays)
		started := finished.Add(-time.Minute)
		logPath := filepath.Join(logDir, "task_"+id+".log")
		if err := os.WriteFile(logPath, []byte(id+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		task := &models.SoftwareTask{
			ID:               id,
			Operation:        "install",
			Component:        "nginx",
			SoftwareKey:      "webserver",
			RequestedVersion: "1.28.2",
			Status:           models.SoftwareTaskStatusSucceeded,
			Phase:            models.SoftwareTaskStatusSucceeded,
			Progress:         100,
			RollbackStatus:   models.SoftwareTaskRollbackNotRequired,
			RequestedBy:      1,
			EventSeq:         1,
			LogPath:          logPath,
			StartedAt:        &started,
			FinishedAt:       &finished,
			CreatedAt:        started,
			UpdatedAt:        finished,
		}
		if err := db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.SoftwareTaskEvent{
			TaskID:    id,
			Seq:       1,
			Type:      "terminal",
			Level:     "info",
			Status:    models.SoftwareTaskStatusSucceeded,
			Phase:     models.SoftwareTaskStatusSucceeded,
			Progress:  100,
			CreatedAt: finished,
		}).Error; err != nil {
			t.Fatal(err)
		}
		return logPath
	}

	oldLog := createFinishedTask("old-task", 100)
	summaryLog := createFinishedTask("summary-task", 40)
	recentLog := createFinishedTask("recent-task", 10)
	cleaner, err := NewCleaner(manager, 90, 30, "30 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	cleaner.now = func() time.Time { return now }
	result, err := cleaner.RunNow()
	if err != nil {
		t.Fatal(err)
	}
	if result.LogFilesDeleted != 2 || result.EventsDeleted != 2 || result.TasksDeleted != 1 {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	if _, err := os.Stat(oldLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old log still exists: %v", err)
	}
	if _, err := os.Stat(summaryLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary-only log still exists: %v", err)
	}
	if _, err := os.Stat(recentLog); err != nil {
		t.Fatalf("recent log was removed: %v", err)
	}
	var oldCount int64
	if err := db.Model(&models.SoftwareTask{}).Where("id = ?", "old-task").Count(&oldCount).Error; err != nil {
		t.Fatal(err)
	}
	if oldCount != 0 {
		t.Fatal("expired task summary was not deleted")
	}
	var summary models.SoftwareTask
	if err := db.First(&summary, "id = ?", "summary-task").Error; err != nil {
		t.Fatal(err)
	}
	if summary.LogPath != "" {
		t.Fatalf("expired log path was not cleared: %q", summary.LogPath)
	}
	var summaryEvents int64
	if err := db.Model(&models.SoftwareTaskEvent{}).
		Where("task_id = ?", "summary-task").
		Count(&summaryEvents).Error; err != nil {
		t.Fatal(err)
	}
	if summaryEvents != 0 {
		t.Fatalf("expired task events = %d, want 0", summaryEvents)
	}
}

func TestManagerStatsScopesUsersAndAggregatesFailures(t *testing.T) {
	db := openTaskTestDB(t)
	manager := NewManager(db, t.TempDir(), func(
		context.Context,
		InstallRequest,
		string,
		*Reporter,
	) error {
		return nil
	})
	now := time.Now().UTC()
	started := now.Add(-2 * time.Minute)
	tasks := []models.SoftwareTask{
		{
			ID: "success", Operation: "install", Component: "nginx", SoftwareKey: "webserver",
			RequestedVersion: "1.28.2", Status: models.SoftwareTaskStatusSucceeded,
			Phase: models.SoftwareTaskStatusSucceeded, RollbackStatus: models.SoftwareTaskRollbackNotRequired,
			RequestedBy: 7, StartedAt: &started, FinishedAt: &now, CreatedAt: started, UpdatedAt: now,
		},
		{
			ID: "failure", Operation: "install", Component: "nginx", SoftwareKey: "webserver",
			RequestedVersion: "1.28.2", Status: models.SoftwareTaskStatusFailed,
			Phase: models.SoftwareTaskStatusFailed, RollbackStatus: models.SoftwareTaskRollbackSucceeded,
			ErrorCode: "VERIFY_FAILED", RequestedBy: 7, StartedAt: &started, FinishedAt: &now,
			CreatedAt: started, UpdatedAt: now,
		},
		{
			ID: "other-user", Operation: "install", Component: "redis", SoftwareKey: "redis",
			RequestedVersion: "7.4.8", Status: models.SoftwareTaskStatusSucceeded,
			Phase: models.SoftwareTaskStatusSucceeded, RollbackStatus: models.SoftwareTaskRollbackNotRequired,
			RequestedBy: 8, StartedAt: &started, FinishedAt: &now, CreatedAt: started, UpdatedAt: now,
		},
	}
	for i := range tasks {
		if err := db.Create(&tasks[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	stats, err := manager.Stats(TaskStatsOptions{RequestedBy: 7, Days: 30})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 2 || stats.Succeeded != 1 || stats.Failed != 1 || stats.SuccessRate != 50 {
		t.Fatalf("unexpected scoped stats: %#v", stats)
	}
	if len(stats.Components) != 1 || stats.Components[0].Component != "nginx" {
		t.Fatalf("unexpected component stats: %#v", stats.Components)
	}
	if len(stats.ErrorCodes) != 1 || stats.ErrorCodes[0].Code != "VERIFY_FAILED" {
		t.Fatalf("unexpected error stats: %#v", stats.ErrorCodes)
	}
}
