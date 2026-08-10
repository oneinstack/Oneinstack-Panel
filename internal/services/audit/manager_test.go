package audit

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.AuditEvent{}, &models.AuditCheckpoint{}, &models.AuditChainState{}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAuditChainDetectsTamperingAndRejectsModelMutation(t *testing.T) {
	manager := newTestManager(t)
	first, err := manager.Append(EventInput{
		RequestID: "request-1", EventType: "auth", Action: "auth.login",
		Method: "POST", Route: "/v1/login", Path: "/v1/login",
		Status: 401, Outcome: "failure", Sensitive: true,
		Username: "operator", Message: "Unauthorized",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Append(EventInput{
		RequestID: "request-2", Action: "post /v1/soft/install",
		Method: "POST", Route: "/v1/soft/install", Path: "/v1/soft/install",
		Status: 200, Outcome: "success", Sensitive: true, UserID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != first.Sequence+1 || second.PreviousHash != first.EntryHash {
		t.Fatalf("audit chain is not continuous: first=%#v second=%#v", first, second)
	}
	verification, err := manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Valid || verification.CheckedEntries != 2 {
		t.Fatalf("unexpected verification: %#v", verification)
	}
	if err := manager.db.Model(&models.AuditEvent{}).
		Where("id = ?", first.ID).
		Update("message", "changed").Error; err == nil {
		t.Fatal("normal model update unexpectedly mutated an append-only audit event")
	}
	if err := manager.db.Exec("UPDATE audit_events SET message = ? WHERE id = ?", "tampered", first.ID).Error; err != nil {
		t.Fatal(err)
	}
	verification, err = manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if verification.Valid || verification.InvalidSequence != first.Sequence {
		t.Fatalf("tampering was not detected: %#v", verification)
	}
}

func TestRetentionCheckpointPreservesChainContinuity(t *testing.T) {
	manager := newTestManager(t)
	now := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	var events []*models.AuditEvent
	for index, age := range []time.Duration{90 * 24 * time.Hour, 60 * 24 * time.Hour, 24 * time.Hour} {
		event, err := manager.Append(EventInput{
			RequestID: "retention", Action: "test.retention", Status: 200,
			Outcome: "success", CreatedAt: now.Add(-age),
			Message: strings.Repeat("x", index+1),
		})
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	result, err := manager.CleanupBefore(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.DeletedEntries != 2 || result.CheckpointSequence != events[1].Sequence {
		t.Fatalf("unexpected cleanup result: %#v", result)
	}
	verification, err := manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if !verification.Valid || verification.CheckedEntries != 1 ||
		verification.CheckpointSequence != events[1].Sequence {
		t.Fatalf("retained chain is invalid: %#v", verification)
	}
	appended, err := manager.Append(EventInput{
		RequestID: "after-cleanup", Action: "test.append", Status: 200, Outcome: "success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Sequence != events[2].Sequence+1 || appended.PreviousHash != events[2].EntryHash {
		t.Fatalf("append did not continue the retained chain: %#v", appended)
	}
}

func TestAuditChainDetectsTailDeletion(t *testing.T) {
	manager := newTestManager(t)
	for index := 0; index < 3; index++ {
		if _, err := manager.Append(EventInput{
			RequestID: "tail-delete", Action: "test.tail", Status: 200,
			Outcome: "success", Message: strings.Repeat("x", index+1),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.db.Exec("DELETE FROM audit_events WHERE sequence = 3").Error; err != nil {
		t.Fatal(err)
	}
	verification, err := manager.Verify()
	if err != nil {
		t.Fatal(err)
	}
	if verification.Valid || !strings.Contains(verification.Message, "链头") {
		t.Fatalf("tail deletion was not detected: %#v", verification)
	}
	if _, err := manager.Append(EventInput{
		RequestID: "after-tail-delete", Action: "test.append", Status: 200, Outcome: "success",
	}); err == nil {
		t.Fatal("append continued after an invalid chain head")
	}
}

func TestAuditQueriesAreBoundedAndEscapeWildcards(t *testing.T) {
	manager := newTestManager(t)
	for _, username := range []string{"ops_100%", "opsA100Z"} {
		if _, err := manager.Append(EventInput{
			RequestID: username, Action: "test.query", Status: 200,
			Outcome: "success", Username: username,
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := manager.List(Filter{Username: "ops_100%", PageSize: 500})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.PageSize != maxPageSize ||
		result.Items[0].Username != "ops_100%" {
		t.Fatalf("unexpected escaped query result: %#v", result)
	}
}

func TestAuditQuerySearchesTerminalCommandMessage(t *testing.T) {
	manager := newTestManager(t)
	if _, err := manager.Append(EventInput{
		RequestID: "terminal-session", EventType: "terminal",
		Action: "terminal.command.submit", Method: "PTY",
		Status: 200, Outcome: "success", Username: "root",
		Message: "command=systemctl restart nginx",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Append(EventInput{
		RequestID: "request-2", Action: "get /v1/dashboard",
		Method: "GET", Status: 200, Outcome: "success",
	}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.List(Filter{Query: "restart nginx"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].Message != "command=systemctl restart nginx" {
		t.Fatalf("terminal command message was not searchable: %#v", result)
	}
}
