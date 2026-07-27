package log

import (
	"context"
	"strings"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func runtimeTestManager(t *testing.T) *Manager {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.RuntimeLogEntry{}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewRuntimeManager(database, 30, "10 5 * * *")
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestRuntimeLogAppendSanitizesAndFilters(t *testing.T) {
	manager := runtimeTestManager(t)
	now := time.Date(2026, 7, 26, 16, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	first, err := manager.Append(context.Background(), EntryInput{
		Level: "error", Source: "panel",
		Message: `request failed password="plain-secret" Authorization: Bearer abc.def.ghi`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Message, "plain-secret") ||
		strings.Contains(first.Message, "abc.def.ghi") ||
		!strings.Contains(first.Message, "[REDACTED]") {
		t.Fatalf("runtime log was not sanitized: %q", first.Message)
	}
	if _, err := manager.Append(context.Background(), EntryInput{
		Level: "info", Source: "http", Message: "GET /health/live 200 1ms",
	}); err != nil {
		t.Fatal(err)
	}
	literal, err := manager.Append(context.Background(), EntryInput{
		Level: "info", Source: "panel", Message: "disk usage 100%_safe",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Query(QueryFilter{
		Level: "error", Source: "panel", Query: "request", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 || result.Items[0].ID != first.ID {
		t.Fatalf("unexpected filtered logs: %#v", result)
	}
	literalResult, err := manager.Query(QueryFilter{Query: "%_", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(literalResult.Items) != 1 || literalResult.Items[0].ID != literal.ID {
		t.Fatalf("LIKE wildcard characters were not treated literally: %#v", literalResult)
	}
}

func TestRuntimeLogQueryUsesStableCursors(t *testing.T) {
	manager := runtimeTestManager(t)
	started := time.Date(2026, 7, 26, 16, 30, 0, 0, time.UTC)
	for index := 1; index <= 5; index++ {
		if _, err := manager.Append(context.Background(), EntryInput{
			OccurredAt: started.Add(time.Duration(index) * time.Second),
			Level:      LevelInfo, Source: "panel", Message: "entry",
		}); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := manager.Query(QueryFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(latest.Items) != 2 || latest.Items[0].ID != 4 ||
		latest.Items[1].ID != 5 || !latest.HasMore ||
		latest.OldestID != 4 || latest.NextCursor != 5 {
		t.Fatalf("unexpected latest page: %#v", latest)
	}
	older, err := manager.Query(QueryFilter{BeforeID: 4, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(older.Items) != 2 || older.Items[0].ID != 2 || older.Items[1].ID != 3 {
		t.Fatalf("unexpected older page: %#v", older)
	}
	newer, err := manager.Query(QueryFilter{AfterID: 2, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(newer.Items) != 2 || newer.Items[0].ID != 3 ||
		newer.Items[1].ID != 4 || !newer.HasMore {
		t.Fatalf("unexpected newer page: %#v", newer)
	}
}

func TestRuntimeWriterFlushesOnStopAndInfersLevels(t *testing.T) {
	manager := runtimeTestManager(t)
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	writer := manager.Writer("panel")
	_, _ = writer.Write([]byte("service started\nwarning: queue nearly full\nfatal error: stopped\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := manager.Query(QueryFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 3 ||
		result.Items[0].Level != LevelInfo ||
		result.Items[1].Level != LevelWarning ||
		result.Items[2].Level != LevelError {
		t.Fatalf("unexpected writer entries: %#v", result.Items)
	}
	// Stop is deliberately idempotent so multiple lifecycle defers are safe.
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestSlowRuntimeSubscriberDisconnectsAndCanResume(t *testing.T) {
	manager := runtimeTestManager(t)
	subscription, cancel := manager.Subscribe(1)
	defer cancel()
	first, err := manager.Append(context.Background(), EntryInput{
		Level: LevelInfo, Source: "panel", Message: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Append(context.Background(), EntryInput{
		Level: LevelInfo, Source: "panel", Message: "second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry, open := <-subscription; !open || entry.ID != first.ID {
		t.Fatalf("unexpected buffered subscription entry: %#v open=%v", entry, open)
	}
	if _, open := <-subscription; open {
		t.Fatal("backpressured subscriber was not disconnected")
	}
	resumed, err := manager.Query(QueryFilter{AfterID: first.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Items) != 1 || resumed.Items[0].ID != second.ID {
		t.Fatalf("subscriber could not resume: %#v", resumed)
	}
}

func TestRuntimeLogCleanupAndStats(t *testing.T) {
	manager := runtimeTestManager(t)
	now := time.Date(2026, 7, 26, 17, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if _, err := manager.Append(context.Background(), EntryInput{
		OccurredAt: now.AddDate(0, 0, -31),
		Level:      LevelError, Source: "panel", Message: "expired",
	}); err != nil {
		t.Fatal(err)
	}
	recent, err := manager.Append(context.Background(), EntryInput{
		OccurredAt: now.Add(-time.Hour),
		Level:      LevelInfo, Source: "http", Message: "recent",
	})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := manager.Cleanup()
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	stats, err := manager.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.Last24Hours != 1 ||
		stats.ErrorCount != 0 || stats.LatestID != recent.ID ||
		len(stats.Sources) != 1 || stats.Sources[0].Source != "http" {
		t.Fatalf("unexpected runtime stats: %#v", stats)
	}
}

func TestRuntimeLogFilterValidation(t *testing.T) {
	manager := runtimeTestManager(t)
	if _, err := manager.Query(QueryFilter{AfterID: 1, BeforeID: 2}); err == nil {
		t.Fatal("conflicting cursors were accepted")
	}
	if _, err := manager.Query(QueryFilter{Level: "verbose"}); err == nil {
		t.Fatal("unsupported log level was accepted")
	}
	if _, err := manager.Query(QueryFilter{Source: "../panel"}); err == nil {
		t.Fatal("unsafe log source was accepted")
	}
	if _, err := NewRuntimeManager(manager.db, 30, "not a cron"); err == nil {
		t.Fatal("invalid cleanup schedule was accepted")
	}
}
