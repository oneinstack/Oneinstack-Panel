package cluster

import (
	"encoding/json"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:cluster-service-test?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.ClusterNode{}, &models.ClusterNodeMetric{}, &models.ClusterTask{}); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRecoverStaleTasksRequeuesAndFailsByAttemptBudget(t *testing.T) {
	m := testManager(t)
	now := time.Now().Add(-time.Hour)
	tasks := []models.ClusterTask{
		{NodeID: 1, Type: "website.sync", IdempotencyKey: "stale-requeue", Payload: `{}`, Status: models.ClusterTaskStatusRunning, Attempts: 1, MaxAttempts: 3, QueuedAt: now, StartedAt: &now},
		{NodeID: 1, Type: "website.sync", IdempotencyKey: "stale-fail", Payload: `{}`, Status: models.ClusterTaskStatusRunning, Attempts: 3, MaxAttempts: 3, QueuedAt: now, StartedAt: &now},
	}
	if err := m.db.Create(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if err := m.RecoverStaleTasks(5 * time.Minute); err != nil {
		t.Fatal(err)
	}
	var first, second models.ClusterTask
	m.db.First(&first, tasks[0].ID)
	m.db.First(&second, tasks[1].ID)
	if first.Status != models.ClusterTaskStatusQueued || second.Status != models.ClusterTaskStatusFailed {
		t.Fatalf("unexpected recovery states: first=%s second=%s", first.Status, second.Status)
	}
}

func TestNodeRegistrationAndHeartbeat(t *testing.T) {
	m := testManager(t)
	created, err := m.CreateNode(CreateNodeInput{Name: "edge-1", Endpoint: "https://edge.example.com/", Group: "prod", Tags: "cn-east"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.Node.TokenHash == "" || created.Node.TokenHash == created.Token {
		t.Fatal("token must be returned once while only its hash is persisted")
	}
	registered, err := m.RegisterNode(NodeRegistration{Token: created.Token, Hostname: "edge-host", PanelVersion: "1.0.0"})
	if err != nil || registered.Status != models.ClusterNodeStatusOnline {
		t.Fatalf("register failed: node=%+v err=%v", registered, err)
	}
	updated, err := m.Heartbeat(NodeHeartbeat{Token: created.Token, CPUPercent: 120, MemoryPercent: 42, DiskPercent: -1, NetworkRecvBPS: 100, UptimeSeconds: 9})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CPUPercent != 100 || updated.DiskPercent != 0 {
		t.Fatalf("metrics were not normalized: %+v", updated)
	}
	var count int64
	if err := m.db.Model(&models.ClusterNodeMetric{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("expected one metric sample, count=%d err=%v", count, err)
	}
}

func TestRotateTokenInvalidatesOldToken(t *testing.T) {
	m := testManager(t)
	created, err := m.CreateNode(CreateNodeInput{Name: "edge-2", Endpoint: "http://127.0.0.1:8089"})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := m.RotateToken(created.Node.ID)
	if err != nil || rotated.Token == created.Token {
		t.Fatalf("token rotation failed: err=%v", err)
	}
	if _, err := m.RegisterNode(NodeRegistration{Token: created.Token}); err != ErrInvalidToken {
		t.Fatalf("old token should be invalid, got %v", err)
	}
	if _, err := m.RegisterNode(NodeRegistration{Token: rotated.Token}); err != nil {
		t.Fatalf("new token should register: %v", err)
	}
}

func TestTaskClaimCompletionAndRetry(t *testing.T) {
	m := testManager(t)
	created, err := m.CreateNode(CreateNodeInput{Name: "edge-task", Endpoint: "http://127.0.0.1:8089"})
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]interface{}{"websiteId": 12, "action": "sync"})
	task, err := m.EnqueueTask(EnqueueTaskInput{NodeID: created.Node.ID, Type: "website.sync", Payload: payload, IdempotencyKey: "sync-12"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := m.EnqueueTask(EnqueueTaskInput{NodeID: created.Node.ID, Type: "website.sync", Payload: payload, IdempotencyKey: "sync-12"})
	if err != nil || duplicate.ID != task.ID {
		t.Fatalf("idempotency failed: duplicate=%+v err=%v", duplicate, err)
	}
	claimed, err := m.ClaimTask(created.Token)
	if err != nil || claimed == nil || claimed.Status != models.ClusterTaskStatusRunning {
		t.Fatalf("claim failed: task=%+v err=%v", claimed, err)
	}
	completed, err := m.CompleteTask(TaskCompletion{Token: created.Token, TaskID: claimed.ID, Status: models.ClusterTaskStatusFailed, Error: "temporary"})
	if err != nil || completed.Status != models.ClusterTaskStatusQueued {
		t.Fatalf("retry requeue failed: task=%+v err=%v", completed, err)
	}
	claimed, err = m.ClaimTask(created.Token)
	if err != nil || claimed == nil {
		t.Fatalf("second claim failed: %v", err)
	}
	completed, err = m.CompleteTask(TaskCompletion{Token: created.Token, TaskID: claimed.ID, Status: models.ClusterTaskStatusSucceeded, Result: json.RawMessage(`{"ok":true}`)})
	if err != nil || completed.Status != models.ClusterTaskStatusSucceeded {
		t.Fatalf("completion failed: task=%+v err=%v", completed, err)
	}
}
