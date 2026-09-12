package cluster

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

var (
	ErrTaskNotFound = gorm.ErrRecordNotFound
	ErrTaskState    = errors.New("invalid task state")
	ErrTaskType     = errors.New("task type is required")
)

type EnqueueTaskInput struct {
	NodeID         uint            `json:"nodeId"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	MaxAttempts    int             `json:"maxAttempts,omitempty"`
}

type TaskCompletion struct {
	Token  string          `json:"token"`
	TaskID uint64          `json:"taskId"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func (m *Manager) EnqueueTask(input EnqueueTaskInput) (models.ClusterTask, error) {
	if input.NodeID == 0 {
		return models.ClusterTask{}, errors.New("node id is required")
	}
	if strings.TrimSpace(input.Type) == "" || len(input.Type) > 120 {
		return models.ClusterTask{}, ErrTaskType
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return models.ClusterTask{}, errors.New("payload must be valid JSON")
	}
	if _, err := m.GetNode(input.NodeID); err != nil {
		return models.ClusterTask{}, err
	}
	maxAttempts := input.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > 10 {
		maxAttempts = 3
	}
	task := models.ClusterTask{NodeID: input.NodeID, Type: strings.TrimSpace(input.Type), IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), Payload: string(input.Payload), Status: models.ClusterTaskStatusQueued, MaxAttempts: maxAttempts, QueuedAt: time.Now()}
	if task.IdempotencyKey != "" {
		var existing models.ClusterTask
		if err := m.db.Where("idempotency_key = ?", task.IdempotencyKey).First(&existing).Error; err == nil {
			return existing, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return models.ClusterTask{}, err
		}
	}
	if err := m.db.Create(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	return task, nil
}

func (m *Manager) ListTasks(nodeID uint, limit int) ([]models.ClusterTask, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var tasks []models.ClusterTask
	err := m.db.Where("node_id = ?", nodeID).Order("id desc").Limit(limit).Find(&tasks).Error
	return tasks, err
}

func (m *Manager) ClaimTask(token string) (*models.ClusterTask, error) {
	node, err := m.findByToken(token)
	if err != nil {
		return nil, err
	}
	if !node.Enabled {
		return nil, ErrNodeDisabled
	}
	// A crashed agent can leave a task in running forever. Requeue stale
	// attempts before claiming the next task so the queue remains recoverable.
	if err := m.RecoverStaleTasks(15 * time.Minute); err != nil {
		return nil, err
	}
	var task models.ClusterTask
	err = m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("node_id = ? AND status = ?", node.ID, models.ClusterTaskStatusQueued).Order("id asc").First(&task).Error; err != nil {
			return err
		}
		now := time.Now()
		task.Status, task.Attempts, task.StartedAt = models.ClusterTaskStatusRunning, task.Attempts+1, &now
		return tx.Save(&task).Error
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// RecoverStaleTasks requeues interrupted attempts and permanently fails tasks
// that exhausted their retry budget. It is safe to call from every agent poll.
func (m *Manager) RecoverStaleTasks(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	cutoff := time.Now().Add(-timeout)
	var stale []models.ClusterTask
	if err := m.db.Where("status = ? AND started_at IS NOT NULL AND started_at < ?", models.ClusterTaskStatusRunning, cutoff).Find(&stale).Error; err != nil {
		return err
	}
	for i := range stale {
		task := &stale[i]
		if task.Attempts < task.MaxAttempts {
			task.Status = models.ClusterTaskStatusQueued
			task.QueuedAt = time.Now()
			task.StartedAt = nil
		} else {
			task.Status = models.ClusterTaskStatusFailed
			task.Error = "task timed out after maximum attempts"
			now := time.Now()
			task.FinishedAt = &now
		}
		if err := m.db.Save(task).Error; err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) CompleteTask(input TaskCompletion) (models.ClusterTask, error) {
	node, err := m.findByToken(input.Token)
	if err != nil {
		return models.ClusterTask{}, err
	}
	var task models.ClusterTask
	if err := m.db.Where("id = ? AND node_id = ?", input.TaskID, node.ID).First(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	if task.Status != models.ClusterTaskStatusRunning {
		return task, ErrTaskState
	}
	if input.Status != models.ClusterTaskStatusSucceeded && input.Status != models.ClusterTaskStatusFailed {
		return task, ErrTaskState
	}
	now := time.Now()
	task.Result, task.Error = string(input.Result), strings.TrimSpace(input.Error)
	task.FinishedAt = &now
	if input.Status == models.ClusterTaskStatusFailed && task.Attempts < task.MaxAttempts {
		task.Status, task.FinishedAt = models.ClusterTaskStatusQueued, nil
		task.QueuedAt = now
	} else {
		task.Status = input.Status
	}
	if err := m.db.Save(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	return task, nil
}
