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
	ErrTaskNotFound    = gorm.ErrRecordNotFound
	ErrTaskState       = errors.New("invalid task state")
	ErrTaskType        = errors.New("task type is required")
	ErrNodeUnavailable = errors.New("node must be enabled and online with a fresh heartbeat")
)

type ClusterTaskSummary struct {
	ID            uint64     `json:"id"`
	NodeID        uint       `json:"nodeId"`
	Type          string     `json:"type"`
	WebsiteID     int64      `json:"websiteId,omitempty"`
	WebsiteName   string     `json:"websiteName,omitempty"`
	WebsiteDomain string     `json:"websiteDomain,omitempty"`
	WebsiteType   string     `json:"websiteType,omitempty"`
	Status        string     `json:"status"`
	Stage         string     `json:"stage,omitempty"`
	Progress      int        `json:"progress"`
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"maxAttempts"`
	Error         string     `json:"error,omitempty"`
	ErrorCode     string     `json:"errorCode,omitempty"`
	QueuedAt      time.Time  `json:"queuedAt"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// ClusterTaskEvent is a safe, payload-free task timeline entry for the
// controller UI. Stage and status are stable machine values localized by the
// client; Message contains only a bounded public summary.
type ClusterTaskEvent struct {
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	Attempt    int       `json:"attempt,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
	Message    string    `json:"message,omitempty"`
}

type ClusterTaskDetail struct {
	ClusterTaskSummary
	Progress int                `json:"progress"`
	Events   []ClusterTaskEvent `json:"events"`
}

type TaskList struct {
	Items    []ClusterTaskSummary `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

type EnqueueTaskInput struct {
	NodeID         uint            `json:"nodeId"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	MaxAttempts    int             `json:"maxAttempts,omitempty"`
	internal       bool
}

type TaskCompletion struct {
	Token  string          `json:"token"`
	TaskID uint64          `json:"taskId"`
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type TaskProgressInput struct {
	Token        string `json:"token,omitempty"`
	TaskID       uint64 `json:"taskId"`
	Stage        string `json:"stage"`
	Progress     int    `json:"progress"`
	LeaseSeconds int    `json:"leaseSeconds,omitempty"`
}

func (m *Manager) EnqueueTask(input EnqueueTaskInput) (models.ClusterTask, error) {
	if input.NodeID == 0 {
		return models.ClusterTask{}, errors.New("node id is required")
	}
	if strings.TrimSpace(input.Type) == "" || len(input.Type) > 120 {
		return models.ClusterTask{}, ErrTaskType
	}
	if isPanelUpdateTask(strings.TrimSpace(input.Type)) && !input.internal {
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
	task := models.ClusterTask{NodeID: input.NodeID, Type: strings.TrimSpace(input.Type), IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), Payload: string(input.Payload), Status: models.ClusterTaskStatusQueued, Stage: "queued", Progress: 10, MaxAttempts: maxAttempts, QueuedAt: time.Now()}
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

func (m *Manager) ListTasks(nodeID uint, limit int) ([]ClusterTaskSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var tasks []models.ClusterTask
	err := m.db.Where("node_id = ?", nodeID).Order("id desc").Limit(limit).Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	summaries := make([]ClusterTaskSummary, 0, len(tasks))
	for i := range tasks {
		summaries = append(summaries, SummarizeTask(tasks[i]))
	}
	return summaries, nil
}

func (m *Manager) ListTasksPage(nodeID uint, page, pageSize int) (*TaskList, error) {
	page, pageSize = normalizeTaskPage(page, pageSize)
	query := m.db.Model(&models.ClusterTask{}).Where("node_id = ?", nodeID)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}

	var tasks []models.ClusterTask
	if err := query.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error; err != nil {
		return nil, err
	}
	summaries := make([]ClusterTaskSummary, 0, len(tasks))
	for i := range tasks {
		summaries = append(summaries, SummarizeTask(tasks[i]))
	}
	return &TaskList{Items: summaries, Total: total, Page: page, PageSize: pageSize}, nil
}

func normalizeTaskPage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func SummarizeTask(task models.ClusterTask) ClusterTaskSummary {
	errorSummary := ""
	if strings.TrimSpace(task.Error) != "" {
		errorSummary = "节点任务执行失败，请查看节点端日志"
	}
	progress := task.Progress
	if progress <= 0 {
		progress = taskProgress(task.Status)
	}
	summary := ClusterTaskSummary{ID: task.ID, NodeID: task.NodeID, Type: task.Type, Status: task.Status, Stage: task.Stage, Progress: progress, Attempts: task.Attempts, MaxAttempts: task.MaxAttempts, Error: errorSummary, QueuedAt: task.QueuedAt, StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if isPanelUpdateTask(task.Type) {
		summary.ErrorCode = safePanelUpdateErrorCode(task.Error)
	}
	if task.Type == "website.sync" || task.Type == "website.content_sync" {
		var payload struct {
			Website models.Website `json:"website"`
		}
		if err := json.Unmarshal([]byte(task.Payload), &payload); err == nil {
			summary.WebsiteID = payload.Website.ID
			summary.WebsiteName = strings.TrimSpace(payload.Website.Name)
			summary.WebsiteDomain = strings.TrimSpace(payload.Website.Domain)
			summary.WebsiteType = strings.TrimSpace(payload.Website.Type)
		}
	}
	return summary
}

func (m *Manager) GetTaskDetail(nodeID uint, taskID uint64) (ClusterTaskDetail, error) {
	var task models.ClusterTask
	if err := m.db.Where("id = ? AND node_id = ?", taskID, nodeID).First(&task).Error; err != nil {
		return ClusterTaskDetail{}, err
	}

	summary := SummarizeTask(task)
	detail := ClusterTaskDetail{
		ClusterTaskSummary: summary,
		Progress:           summary.Progress,
		Events: []ClusterTaskEvent{{
			Stage:      "queued",
			Status:     models.ClusterTaskStatusSucceeded,
			OccurredAt: task.QueuedAt,
		}},
	}
	if task.Attempts > 0 && task.StartedAt == nil && task.Status == models.ClusterTaskStatusQueued {
		detail.Events = append(detail.Events, ClusterTaskEvent{
			Stage:      "retrying",
			Status:     models.ClusterTaskStatusQueued,
			Attempt:    task.Attempts,
			OccurredAt: task.UpdatedAt,
		})
	}
	if task.StartedAt != nil {
		detail.Events = append(detail.Events, ClusterTaskEvent{
			Stage:      "running",
			Status:     models.ClusterTaskStatusSucceeded,
			Attempt:    task.Attempts,
			OccurredAt: *task.StartedAt,
		})
	}
	if task.Stage != "" && task.Stage != "queued" && task.Stage != "running" && task.FinishedAt == nil {
		detail.Events = append(detail.Events, ClusterTaskEvent{
			Stage:      task.Stage,
			Status:     task.Status,
			Attempt:    task.Attempts,
			OccurredAt: task.UpdatedAt,
		})
	}
	if task.FinishedAt != nil {
		event := ClusterTaskEvent{
			Stage:      task.Status,
			Status:     task.Status,
			Attempt:    task.Attempts,
			OccurredAt: *task.FinishedAt,
		}
		if task.Status == models.ClusterTaskStatusFailed {
			event.Message = detail.Error
		}
		detail.Events = append(detail.Events, event)
	}
	return detail, nil
}

func taskProgress(status string) int {
	switch status {
	case models.ClusterTaskStatusQueued:
		return 10
	case models.ClusterTaskStatusRunning:
		return 60
	case models.ClusterTaskStatusSucceeded, models.ClusterTaskStatusFailed, models.ClusterTaskStatusCanceled:
		return 100
	default:
		return 0
	}
}

func (m *Manager) RestartPanel(id uint) (ClusterTaskSummary, error) {
	node, err := m.GetNode(id)
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	if !nodeHeartbeatFresh(node, time.Now()) {
		return ClusterTaskSummary{}, ErrNodeUnavailable
	}
	task, err := m.EnqueueTask(EnqueueTaskInput{NodeID: id, Type: "panel.restart", Payload: json.RawMessage(`{}`), MaxAttempts: 1, IdempotencyKey: "panel-restart:" + time.Now().UTC().Format("20060102T150405.000000000") + ":" + stringID(id)})
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	return SummarizeTask(task), nil
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
		leaseDuration := 15 * time.Minute
		if task.Type == TaskPanelUpdateApply {
			leaseDuration = 40 * time.Minute
		}
		leaseExpiresAt := now.Add(leaseDuration)
		task.Status, task.Stage, task.Progress = models.ClusterTaskStatusRunning, "running", 20
		task.Attempts, task.StartedAt, task.LeaseExpiresAt = task.Attempts+1, &now, &leaseExpiresAt
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
	now := time.Now()
	cutoff := now.Add(-timeout)
	var stale []models.ClusterTask
	if err := m.db.Where(
		"status = ? AND ((lease_expires_at IS NOT NULL AND lease_expires_at < ?) OR (lease_expires_at IS NULL AND started_at IS NOT NULL AND started_at < ?))",
		models.ClusterTaskStatusRunning,
		now,
		cutoff,
	).Find(&stale).Error; err != nil {
		return err
	}
	for i := range stale {
		task := &stale[i]
		if task.Attempts < task.MaxAttempts {
			task.Status = models.ClusterTaskStatusQueued
			task.Stage = "retrying"
			task.Progress = 10
			task.QueuedAt = time.Now()
			task.StartedAt = nil
			task.LeaseExpiresAt = nil
		} else {
			task.Status = models.ClusterTaskStatusFailed
			task.Stage = models.ClusterTaskStatusFailed
			task.Progress = 100
			if isPanelUpdateTask(task.Type) {
				task.Error = panelUpdateErrorResultUnknown
			} else {
				task.Error = "task timed out after maximum attempts"
			}
			now := time.Now()
			task.FinishedAt = &now
			task.LeaseExpiresAt = nil
		}
		if err := m.db.Save(task).Error; err != nil {
			return err
		}
		if task.Type == TaskPanelUpdateApply && task.Status == models.ClusterTaskStatusFailed {
			recordPanelUpdateAudit(*task, "failure")
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
	if input.Status != models.ClusterTaskStatusSucceeded && input.Status != models.ClusterTaskStatusFailed {
		return task, ErrTaskState
	}
	if isPanelUpdateTask(task.Type) {
		input, err = sanitizePanelUpdateCompletion(node, task, input)
		if err != nil {
			return task, err
		}
	}
	if task.Status != models.ClusterTaskStatusRunning {
		if task.Status == input.Status {
			return task, nil
		}
		return task, ErrTaskState
	}
	now := time.Now()
	task.Result, task.Error = string(input.Result), strings.TrimSpace(input.Error)
	task.FinishedAt = &now
	task.Stage = input.Status
	task.Progress = 100
	task.LeaseExpiresAt = nil
	if input.Status == models.ClusterTaskStatusFailed && task.Attempts < task.MaxAttempts {
		task.Status, task.FinishedAt = models.ClusterTaskStatusQueued, nil
		task.QueuedAt = now
	} else {
		task.Status = input.Status
	}
	if err := m.db.Save(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	if task.Type == TaskPanelUpdateApply {
		outcome := "success"
		if task.Status != models.ClusterTaskStatusSucceeded {
			outcome = "failure"
		}
		recordPanelUpdateAudit(task, outcome)
	}
	return task, nil
}

func (m *Manager) ReportTaskProgress(input TaskProgressInput) (models.ClusterTask, error) {
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
	stage := strings.TrimSpace(input.Stage)
	if stage == "" || len(stage) > 64 {
		return task, ErrTaskState
	}
	if isPanelUpdateTask(task.Type) && !validPanelUpdateProgressStage(stage) {
		return task, ErrTaskState
	}
	progress := input.Progress
	if progress < 1 {
		progress = 1
	}
	if progress > 99 {
		progress = 99
	}
	leaseSeconds := input.LeaseSeconds
	if leaseSeconds <= 0 {
		leaseSeconds = 15 * 60
	}
	if leaseSeconds > 40*60 {
		leaseSeconds = 40 * 60
	}
	leaseExpiresAt := time.Now().Add(time.Duration(leaseSeconds) * time.Second)
	updates := map[string]interface{}{
		"stage":            stage,
		"progress":         progress,
		"lease_expires_at": leaseExpiresAt,
	}
	result := m.db.Model(&models.ClusterTask{}).
		Where("id = ? AND node_id = ? AND status = ?", task.ID, node.ID, models.ClusterTaskStatusRunning).
		Updates(updates)
	if result.Error != nil {
		return task, result.Error
	}
	if result.RowsAffected == 0 {
		return task, ErrTaskState
	}
	task.Stage, task.Progress, task.LeaseExpiresAt = stage, progress, &leaseExpiresAt
	task.UpdatedAt = time.Now()
	return task, nil
}
