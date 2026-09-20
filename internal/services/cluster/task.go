package cluster

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTaskNotFound    = gorm.ErrRecordNotFound
	ErrTaskState       = errors.New("invalid task state")
	ErrTaskType        = errors.New("task type is required")
	ErrNodeUnavailable = errors.New("node must be enabled and online with a fresh heartbeat")
)

type ClusterTaskSummary struct {
	ID                     uint64     `json:"id"`
	NodeID                 uint       `json:"nodeId"`
	BatchID                string     `json:"batchId,omitempty"`
	Type                   string     `json:"type"`
	WebsiteID              int64      `json:"websiteId,omitempty"`
	WebsiteName            string     `json:"websiteName,omitempty"`
	WebsiteDomain          string     `json:"websiteDomain,omitempty"`
	WebsiteType            string     `json:"websiteType,omitempty"`
	Status                 string     `json:"status"`
	Stage                  string     `json:"stage,omitempty"`
	Progress               int        `json:"progress"`
	Attempts               int        `json:"attempts"`
	MaxAttempts            int        `json:"maxAttempts"`
	RequestedBy            int64      `json:"requestedBy,omitempty"`
	CancelRequested        bool       `json:"cancelRequested"`
	Cancelable             bool       `json:"cancelable"`
	Error                  string     `json:"error,omitempty"`
	ErrorCode              string     `json:"errorCode,omitempty"`
	DiagnosisOverallStatus string     `json:"diagnosisOverallStatus,omitempty"`
	QueuedAt               time.Time  `json:"queuedAt"`
	StartedAt              *time.Time `json:"startedAt,omitempty"`
	FinishedAt             *time.Time `json:"finishedAt,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	UpdatedAt              time.Time  `json:"updatedAt"`
}

// ClusterTaskEvent is a safe, payload-free task timeline entry for the
// controller UI. Stage and status are stable machine values localized by the
// client; Message contains only a bounded public summary.
type ClusterTaskEvent struct {
	Sequence   uint64    `json:"sequence"`
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	Level      string    `json:"level,omitempty"`
	Code       string    `json:"code,omitempty"`
	Progress   int       `json:"progress"`
	Attempt    int       `json:"attempt,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
	Message    string    `json:"message,omitempty"`
}

type ClusterTaskDetail struct {
	ClusterTaskSummary
	Progress int                `json:"progress"`
	Events   []ClusterTaskEvent `json:"events"`
	Result   json.RawMessage    `json:"result,omitempty"`
}

type TaskList struct {
	Items    []ClusterTaskSummary `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

type EnqueueTaskInput struct {
	NodeID         uint            `json:"nodeId"`
	BatchID        string          `json:"batchId,omitempty"`
	Type           string          `json:"type"`
	Payload        json.RawMessage `json:"payload"`
	IdempotencyKey string          `json:"idempotencyKey,omitempty"`
	MaxAttempts    int             `json:"maxAttempts,omitempty"`
	RequestedBy    int64           `json:"requestedBy,omitempty"`
	Cancelable     *bool           `json:"cancelable,omitempty"`
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

type TaskFilter struct {
	NodeID   uint
	BatchID  string
	Type     string
	Status   string
	Page     int
	PageSize int
}

type TaskControl struct {
	TaskID          uint64 `json:"taskId"`
	CancelRequested bool   `json:"cancelRequested"`
	Cancelable      bool   `json:"cancelable"`
}

func (m *Manager) EnqueueTask(input EnqueueTaskInput) (models.ClusterTask, error) {
	if input.NodeID == 0 {
		return models.ClusterTask{}, errors.New("node id is required")
	}
	if strings.TrimSpace(input.Type) == "" || len(input.Type) > 120 {
		return models.ClusterTask{}, ErrTaskType
	}
	if !allowedClusterTaskType(strings.TrimSpace(input.Type)) {
		return models.ClusterTask{}, ErrTaskType
	}
	taskType := strings.TrimSpace(input.Type)
	if (isPanelUpdateTask(taskType) || taskType == "panel.restart" || taskType == TaskNodeDiagnose) && !input.internal {
		return models.ClusterTask{}, ErrTaskType
	}
	if len(input.Payload) == 0 || !json.Valid(input.Payload) {
		return models.ClusterTask{}, errors.New("payload must be valid JSON")
	}
	node, err := m.GetNode(input.NodeID)
	if err != nil {
		return models.ClusterTask{}, err
	}
	offlineDiagnosis := taskType == TaskNodeDiagnose && !nodeHeartbeatFresh(node, time.Now())
	if !offlineDiagnosis && (node.LifecycleStatus == models.ClusterNodeLifecycleDraining || node.LifecycleStatus == models.ClusterNodeLifecycleDisabled || node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete ||
		((node.LifecycleStatus == models.ClusterNodeLifecycleMaintenance || node.LifecycleStatus == models.ClusterNodeLifecycleDrained) && !containsTaskType(maintenanceTaskTypes(), taskType))) {
		return models.ClusterTask{}, ErrNodeLifecycle
	}
	maxAttempts := input.MaxAttempts
	if maxAttempts <= 0 || maxAttempts > 10 {
		maxAttempts = 3
	}
	cancelable := true
	if input.Cancelable != nil {
		cancelable = *input.Cancelable
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	providedIdempotencyKey := idempotencyKey != ""
	if !providedIdempotencyKey {
		idempotencyKey = "auto:" + uuid.NewString()
	}
	task := models.ClusterTask{NodeID: input.NodeID, BatchID: strings.TrimSpace(input.BatchID), Type: taskType, IdempotencyKey: idempotencyKey, Payload: string(input.Payload), Status: models.ClusterTaskStatusQueued, Stage: "queued", Progress: 10, MaxAttempts: maxAttempts, RequestedBy: input.RequestedBy, Cancelable: cancelable, QueuedAt: time.Now()}
	if providedIdempotencyKey {
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
	_ = m.appendTaskEvent(task.ID, "queued", models.ClusterTaskStatusQueued, "info", "task_queued", 10, "任务已进入等待队列")
	return task, nil
}

func (m *Manager) ListAllTasks(filter TaskFilter) (*TaskList, error) {
	filter.Page, filter.PageSize = normalizeTaskPage(filter.Page, filter.PageSize)
	query := m.db.Model(&models.ClusterTask{})
	if filter.NodeID > 0 {
		query = query.Where("node_id = ?", filter.NodeID)
	}
	if value := strings.TrimSpace(filter.BatchID); value != "" {
		query = query.Where("batch_id = ?", value)
	}
	if value := strings.TrimSpace(filter.Type); value != "" {
		query = query.Where("type = ?", value)
	}
	if value := strings.TrimSpace(filter.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.ClusterTask
	if err := query.Order("id desc").Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).Find(&tasks).Error; err != nil {
		return nil, err
	}
	items := make([]ClusterTaskSummary, 0, len(tasks))
	for i := range tasks {
		items = append(items, SummarizeTask(tasks[i]))
	}
	return &TaskList{Items: items, Total: total, Page: filter.Page, PageSize: filter.PageSize}, nil
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
		errorSummary = boundedText(sanitizeDiagnosticText(task.Error), 512)
		if errorSummary == "" {
			errorSummary = "节点任务执行失败，请查看节点端日志"
		}
	}
	progress := task.Progress
	if progress <= 0 {
		progress = taskProgress(task.Status)
	}
	summary := ClusterTaskSummary{ID: task.ID, NodeID: task.NodeID, BatchID: task.BatchID, Type: task.Type, Status: task.Status, Stage: task.Stage, Progress: progress, Attempts: task.Attempts, MaxAttempts: task.MaxAttempts, RequestedBy: task.RequestedBy, CancelRequested: task.CancelRequested, Cancelable: task.Cancelable, Error: errorSummary, QueuedAt: task.QueuedAt, StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt}
	if isPanelUpdateTask(task.Type) {
		summary.ErrorCode = safePanelUpdateErrorCode(task.Error)
	}
	if task.Type == TaskNodeDiagnose && strings.TrimSpace(task.Result) != "" {
		var result DiagnosisResult
		if err := json.Unmarshal([]byte(task.Result), &result); err == nil {
			summary.DiagnosisOverallStatus = result.OverallStatus
		}
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
	events, err := m.ListTaskEvents(nodeID, taskID, 500)
	if err != nil {
		return ClusterTaskDetail{}, err
	}
	detail := ClusterTaskDetail{ClusterTaskSummary: summary, Progress: summary.Progress, Events: events}
	if task.Type == TaskNodeDiagnose && json.Valid([]byte(task.Result)) && strings.TrimSpace(task.Result) != "" {
		detail.Result = json.RawMessage(task.Result)
	}
	return detail, nil
}

func (m *Manager) ListTaskEvents(nodeID uint, taskID uint64, limit int) ([]ClusterTaskEvent, error) {
	var count int64
	if err := m.db.Model(&models.ClusterTask{}).Where("id = ? AND node_id = ?", taskID, nodeID).Count(&count).Error; err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if limit < 1 || limit > 1000 {
		limit = 200
	}
	var rows []models.ClusterTaskEvent
	if err := m.db.Where("task_id = ?", taskID).Order("sequence asc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	events := make([]ClusterTaskEvent, 0, len(rows))
	for _, row := range rows {
		events = append(events, ClusterTaskEvent{Sequence: row.Sequence, Stage: row.Stage, Status: row.Status, Level: row.Level, Code: row.Code, Progress: row.Progress, OccurredAt: row.OccurredAt, Message: row.Message})
	}
	return events, nil
}

func (m *Manager) appendTaskEvent(taskID uint64, stage, status, level, code string, progress int, message string) error {
	message = boundedText(message, 1024)
	return m.db.Transaction(func(tx *gorm.DB) error {
		var task models.ClusterTask
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").First(&task, taskID).Error; err != nil {
			return err
		}
		var sequence uint64
		if err := tx.Model(&models.ClusterTaskEvent{}).Where("task_id = ?", taskID).Select("COALESCE(MAX(sequence), 0)").Scan(&sequence).Error; err != nil {
			return err
		}
		event := models.ClusterTaskEvent{TaskID: taskID, Sequence: sequence + 1, Stage: boundedText(stage, 64), Status: boundedText(status, 16), Level: boundedText(level, 16), Code: boundedText(code, 64), Progress: progress, Message: message, OccurredAt: time.Now().UTC()}
		return tx.Create(&event).Error
	})
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
	if !node.Enabled || node.LifecycleStatus == models.ClusterNodeLifecycleDisabled || node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete {
		return nil, ErrNodeDisabled
	}
	if node.LifecycleStatus == models.ClusterNodeLifecycleDraining {
		_ = m.finishDrainIfIdle(node.ID)
		return nil, nil
	}
	// A crashed agent can leave a task in running forever. Requeue stale
	// attempts before claiming the next task so the queue remains recoverable.
	if err := m.RecoverStaleTasks(15 * time.Minute); err != nil {
		return nil, err
	}
	var task models.ClusterTask
	err = m.db.Transaction(func(tx *gorm.DB) error {
		var currentNode models.ClusterNode
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&currentNode, node.ID).Error; err != nil {
			return err
		}
		if !currentNode.Enabled || currentNode.LifecycleStatus == models.ClusterNodeLifecycleDisabled || currentNode.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete {
			return ErrNodeDisabled
		}
		if currentNode.LifecycleStatus == models.ClusterNodeLifecycleDraining {
			return gorm.ErrRecordNotFound
		}
		query := tx.Where("node_id = ? AND status = ?", currentNode.ID, models.ClusterTaskStatusQueued)
		if currentNode.LifecycleStatus == models.ClusterNodeLifecycleMaintenance || currentNode.LifecycleStatus == models.ClusterNodeLifecycleDrained {
			query = query.Where("type IN ?", maintenanceTaskTypes())
		}
		if err := query.Order("id asc").First(&task).Error; err != nil {
			return err
		}
		if task.BatchID != "" {
			var batch models.ClusterBatchOperation
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ?", task.BatchID).Error; err != nil {
				return err
			}
			var running int64
			if err := tx.Model(&models.ClusterTask{}).Where("batch_id = ? AND status = ?", task.BatchID, models.ClusterTaskStatusRunning).Count(&running).Error; err != nil {
				return err
			}
			if running >= int64(batch.MaxConcurrency) {
				return gorm.ErrRecordNotFound
			}
		}
		now := time.Now()
		leaseDuration := 15 * time.Minute
		if task.Type == TaskPanelUpdateApply {
			leaseDuration = 40 * time.Minute
		}
		leaseExpiresAt := now.Add(leaseDuration)
		task.Status, task.Stage, task.Progress = models.ClusterTaskStatusRunning, "running", 20
		if task.Type == "panel.restart" || task.Type == TaskPanelUpdateApply {
			task.Cancelable = false
		}
		task.Attempts, task.StartedAt, task.LeaseExpiresAt = task.Attempts+1, &now, &leaseExpiresAt
		result := tx.Model(&models.ClusterTask{}).
			Where("id = ? AND status = ?", task.ID, models.ClusterTaskStatusQueued).
			Updates(map[string]any{
				"status": task.Status, "stage": task.Stage, "progress": task.Progress,
				"cancelable": task.Cancelable, "attempts": task.Attempts,
				"started_at": task.StartedAt, "lease_expires_at": task.LeaseExpiresAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = m.appendTaskEvent(task.ID, "running", models.ClusterTaskStatusRunning, "info", "task_started", 20, "节点已领取任务")
	_ = m.updateBatchStatus(task.BatchID)
	return &task, nil
}

func maintenanceTaskTypes() []string {
	return []string{"node.diagnose.v1", "panel.restart", TaskPanelUpdateCheck, TaskPanelUpdateApply}
}

func allowedClusterTaskType(taskType string) bool {
	switch taskType {
	case "software.install", "software.uninstall",
		"service.start", "service.stop", "service.restart", "service.reload",
		"file.upload", "database.sync", "website.sync", "website.content_sync",
		"panel.restart", TaskNodeDiagnose, TaskPanelUpdateCheck, TaskPanelUpdateApply:
		return true
	default:
		return false
	}
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
		if task.Status == models.ClusterTaskStatusQueued {
			_ = m.appendTaskEvent(task.ID, "retrying", task.Status, "warning", "task_requeued", task.Progress, "任务租约超时，已重新排队")
		} else {
			_ = m.appendTaskEvent(task.ID, task.Stage, task.Status, "error", "task_timeout", task.Progress, "任务重试次数已耗尽")
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
	if input.Status != models.ClusterTaskStatusSucceeded && input.Status != models.ClusterTaskStatusFailed && input.Status != models.ClusterTaskStatusCanceled {
		return task, ErrTaskState
	}
	if len(input.Result) > 1<<20 {
		return task, ErrTaskState
	}
	if task.Type == TaskNodeDiagnose && len(input.Result) > 0 && input.Status != models.ClusterTaskStatusCanceled {
		sanitized, sanitizeErr := sanitizeDiagnosisResult(input.Result)
		if sanitizeErr != nil {
			return task, ErrTaskState
		}
		input.Result = sanitized
	}
	if isPanelUpdateTask(task.Type) && input.Status != models.ClusterTaskStatusCanceled {
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
	task.Result = string(input.Result)
	task.Error = boundedText(sanitizeDiagnosticText(input.Error), 1024)
	task.FinishedAt = &now
	task.Stage = input.Status
	task.Progress = 100
	task.LeaseExpiresAt = nil
	if input.Status == models.ClusterTaskStatusFailed && task.Attempts < task.MaxAttempts && !task.CancelRequested {
		task.Status, task.FinishedAt = models.ClusterTaskStatusQueued, nil
		task.QueuedAt = now
	} else {
		task.Status = input.Status
	}
	if err := m.db.Save(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	level := "info"
	code := "task_completed"
	message := "任务执行成功"
	if task.Status == models.ClusterTaskStatusFailed {
		level, code, message = "error", "task_failed", "节点任务执行失败"
	} else if task.Status == models.ClusterTaskStatusCanceled {
		level, code, message = "warning", "task_canceled", "任务已取消"
	} else if task.Status == models.ClusterTaskStatusQueued {
		level, code, message = "warning", "task_retry", "任务执行失败，已重新排队"
	}
	_ = m.appendTaskEvent(task.ID, task.Stage, task.Status, level, code, task.Progress, message)
	_ = m.updateBatchStatus(task.BatchID)
	_ = m.finishDrainIfIdle(task.NodeID)
	if task.Type == TaskPanelUpdateApply {
		outcome := "success"
		if task.Status != models.ClusterTaskStatusSucceeded {
			outcome = "failure"
		}
		recordPanelUpdateAudit(task, outcome)
	}
	return task, nil
}

func containsTaskType(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	_ = m.appendTaskEvent(task.ID, stage, task.Status, "info", "task_progress", progress, "任务进度已更新")
	return task, nil
}

func (m *Manager) CancelTask(nodeID uint, taskID uint64) (ClusterTaskSummary, error) {
	var task models.ClusterTask
	if err := m.db.Where("id = ? AND node_id = ?", taskID, nodeID).First(&task).Error; err != nil {
		return ClusterTaskSummary{}, err
	}
	if task.Status == models.ClusterTaskStatusSucceeded || task.Status == models.ClusterTaskStatusFailed || task.Status == models.ClusterTaskStatusCanceled {
		return SummarizeTask(task), nil
	}
	if !task.Cancelable {
		return ClusterTaskSummary{}, ErrTaskState
	}
	now := time.Now()
	if task.Status == models.ClusterTaskStatusQueued {
		result := m.db.Model(&models.ClusterTask{}).
			Where("id = ? AND node_id = ? AND status = ?", taskID, nodeID, models.ClusterTaskStatusQueued).
			Updates(map[string]interface{}{"status": models.ClusterTaskStatusCanceled, "stage": models.ClusterTaskStatusCanceled, "progress": 100, "cancel_requested": true, "finished_at": now, "lease_expires_at": nil})
		if result.Error != nil {
			return ClusterTaskSummary{}, result.Error
		}
		if result.RowsAffected == 0 {
			return ClusterTaskSummary{}, ErrTaskState
		}
		task.Status, task.Stage, task.Progress, task.CancelRequested, task.FinishedAt = models.ClusterTaskStatusCanceled, models.ClusterTaskStatusCanceled, 100, true, &now
		_ = m.appendTaskEvent(task.ID, task.Stage, task.Status, "warning", "task_canceled", 100, "等待中的任务已取消")
	} else {
		node, nodeErr := m.GetNode(nodeID)
		if nodeErr != nil {
			return ClusterTaskSummary{}, nodeErr
		}
		if !hasCapability(node.Capabilities, CapabilityTaskCancel) {
			return ClusterTaskSummary{}, ErrTaskState
		}
		result := m.db.Model(&models.ClusterTask{}).Where("id = ? AND node_id = ? AND status = ?", taskID, nodeID, models.ClusterTaskStatusRunning).Update("cancel_requested", true)
		if result.Error != nil {
			return ClusterTaskSummary{}, result.Error
		}
		if result.RowsAffected == 0 {
			return ClusterTaskSummary{}, ErrTaskState
		}
		task.CancelRequested = true
		_ = m.appendTaskEvent(task.ID, task.Stage, task.Status, "warning", "cancel_requested", task.Progress, "已请求节点在安全检查点取消任务")
	}
	_ = m.updateBatchStatus(task.BatchID)
	return SummarizeTask(task), nil
}

func (m *Manager) TaskControl(token string, taskID uint64) (TaskControl, error) {
	node, err := m.findByToken(token)
	if err != nil {
		return TaskControl{}, err
	}
	var task models.ClusterTask
	if err := m.db.Select("id", "cancel_requested", "cancelable").Where("id = ? AND node_id = ?", taskID, node.ID).First(&task).Error; err != nil {
		return TaskControl{}, err
	}
	return TaskControl{TaskID: task.ID, CancelRequested: task.CancelRequested, Cancelable: task.Cancelable}, nil
}
