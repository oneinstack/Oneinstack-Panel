package fail2ban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TaskListOptions struct {
	ActiveOnly bool
	Page       int
	PageSize   int
}

type TaskList struct {
	Data     []models.Fail2banTask `json:"data"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type taskParameters struct {
	PolicyChange *PolicyChangeRequest `json:"policyChange,omitempty"`
	Ban          *BanRequest          `json:"ban,omitempty"`
}

type Manager struct {
	db           *gorm.DB
	service      *Service
	queue        chan string
	once         sync.Once
	ctx          context.Context
	mu           sync.Mutex
	submitMu     sync.Mutex
	banMu        sync.Mutex
	banSince     time.Time
	bansInWindow int
}

var defaultManager = &Manager{}

func DefaultManager() *Manager {
	defaultManager.mu.Lock()
	defer defaultManager.mu.Unlock()
	if defaultManager.db == nil {
		defaultManager.db = app.DB()
		defaultManager.service = NewService(defaultManager.db)
		defaultManager.queue = make(chan string, 128)
	}
	return defaultManager
}

func (m *Manager) Start(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.once.Do(func() {
		m.ctx = ctx
		_ = m.db.Model(&models.Fail2banTask{}).
			Where("status = ?", models.Fail2banTaskRunning).
			Updates(map[string]any{"status": models.Fail2banTaskQueued, "phase": "recovered", "message": "服务重启后重新排队"}).Error
		go m.worker(ctx)
		go m.runCollector(ctx)
		var pending []models.Fail2banTask
		if err := m.db.Where("status = ?", models.Fail2banTaskQueued).Order("created_at ASC").Find(&pending).Error; err == nil {
			for i := range pending {
				m.enqueue(pending[i].ID)
			}
		}
	})
}

func (m *Manager) SubmitPolicyChange(request PolicyChangeRequest, userID int64, requestIP, triggeredBy string) (*models.Fail2banTask, error) {
	request.RequestIP = requestIP
	normalized, policy, err := m.service.NormalizePolicyChange(request, userID)
	if err != nil {
		return nil, err
	}
	parameters, _ := json.Marshal(taskParameters{PolicyChange: &normalized})
	operation := "apply_policy"
	if normalized.Action == "delete" {
		operation = "delete_policy"
	}
	key := digest("policy|" + normalized.Action + "|" + mustJSON(normalized.Policy))
	return m.submit(&models.Fail2banTask{
		ID: uuid.NewString(), Operation: operation, PolicyID: policy.ID,
		IdempotencyKey: key,
		Status:         models.Fail2banTaskQueued, Phase: "queued", Message: "规则变更已排队",
		RequestedBy: userID, TriggeredBy: normalizeTriggeredBy(triggeredBy), ParametersJSON: string(parameters),
	})
}

func (m *Manager) SubmitBan(operation string, request BanRequest, userID int64, requestIP, triggeredBy string) (*models.Fail2banTask, error) {
	request.RequestIP = requestIP
	request, policy, incident, err := m.service.ResolveBanRequest(request)
	if err != nil {
		return nil, err
	}
	if operation != "ban_ip" && operation != "unban_ip" {
		return nil, validation("任务操作无效")
	}
	parameters, _ := json.Marshal(taskParameters{Ban: &request})
	task := &models.Fail2banTask{
		ID: uuid.NewString(), Operation: operation, PolicyID: policy.ID, TargetIP: request.IP,
		IdempotencyKey: digest("ban|" + operation + "|" + mustJSON(request)),
		Status:         models.Fail2banTaskQueued, Phase: "queued", Message: "IP 处置任务已排队",
		RequestedBy: userID, TriggeredBy: normalizeTriggeredBy(triggeredBy), ParametersJSON: string(parameters),
	}
	if incident != nil {
		task.IncidentID = incident.ID
	}
	return m.submit(task)
}

func (m *Manager) SubmitMigration() (*models.Fail2banTask, error) {
	return m.submit(&models.Fail2banTask{
		ID: uuid.NewString(), Operation: "migrate_legacy", IdempotencyKey: "fail2ban|migrate_legacy",
		Status: models.Fail2banTaskQueued, Phase: "queued", Message: "旧自动封禁迁移已排队",
		RequestedBy: 0, TriggeredBy: "system", ParametersJSON: "{}",
	})
}

func (m *Manager) submit(task *models.Fail2banTask) (*models.Fail2banTask, error) {
	if m == nil || m.db == nil {
		return nil, ErrUnavailable
	}
	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	if task.IdempotencyKey != "" {
		var existing models.Fail2banTask
		if err := m.db.Where("idempotency_key = ?", task.IdempotencyKey).First(&existing).Error; err == nil {
			return &existing, nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}
	if err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		return createTaskEvent(tx, task, "queued", "info", "任务已进入持久队列")
	}); err != nil {
		return nil, err
	}
	m.Start(context.Background())
	m.enqueue(task.ID)
	return task, nil
}

func (m *Manager) enqueue(id string) {
	select {
	case m.queue <- id:
	default:
		go func() { m.queue <- id }()
	}
}

func (m *Manager) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			m.execute(ctx, id)
		}
	}
}

func (m *Manager) execute(parent context.Context, id string) {
	var task models.Fail2banTask
	if err := m.db.First(&task, "id = ?", id).Error; err != nil || task.Status != models.Fail2banTaskQueued {
		return
	}
	now := time.Now().UTC()
	if err := m.update(&task, map[string]any{"status": models.Fail2banTaskRunning, "phase": "precheck", "progress": 10, "message": "正在校验实际状态", "started_at": &now}, "running", "info", "开始执行安全任务"); err != nil {
		return
	}
	var params taskParameters
	if err := json.Unmarshal([]byte(task.ParametersJSON), &params); err != nil {
		m.fail(&task, "invalid_parameters", err)
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	defer cancel()
	var err error
	switch task.Operation {
	case "apply_policy", "delete_policy":
		if params.PolicyChange == nil {
			err = validation("规则任务参数缺失")
		} else {
			_, err = m.service.ApplyPolicyChange(ctx, *params.PolicyChange, task.RequestedBy)
		}
	case "migrate_legacy":
		err = m.migrateLegacy(ctx)
	case "ban_ip":
		if params.Ban == nil {
			err = validation("封禁任务参数缺失")
		} else {
			err = m.service.Ban(ctx, *params.Ban, task.ID)
		}
	case "unban_ip":
		if params.Ban == nil {
			err = validation("解封任务参数缺失")
		} else {
			err = m.service.Unban(ctx, *params.Ban)
		}
	default:
		err = validation("不支持的安全任务")
	}
	if err != nil {
		m.fail(&task, taskErrorCode(err), err)
		return
	}
	finished := time.Now().UTC()
	_ = m.update(&task, map[string]any{
		"status": models.Fail2banTaskSucceeded, "phase": "completed", "progress": 100,
		"message": "安全任务执行成功", "finished_at": &finished, "error_code": "", "error_message": "",
	}, "completed", "info", "安全任务执行成功")
}

func (m *Manager) fail(task *models.Fail2banTask, code string, cause error) {
	finished := time.Now().UTC()
	message := taskFailureMessage(cause)
	displayMessage := "安全任务执行失败"
	if message != "" {
		displayMessage = message
	}
	_ = m.update(task, map[string]any{
		"status": models.Fail2banTaskFailed, "phase": "failed", "message": displayMessage,
		"error_code": code, "error_message": message, "finished_at": &finished,
	}, "failed", "error", message)
}

func taskFailureMessage(cause error) string {
	if cause == nil {
		return "安全任务执行失败"
	}
	message := strings.TrimSpace(cause.Error())
	const maxRunes = 500
	runes := []rune(message)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return message
}

func (m *Manager) update(task *models.Fail2banTask, values map[string]any, eventType, level, message string) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Fail2banTask{}).Where("id = ?", task.ID).Updates(values).Error; err != nil {
			return err
		}
		if err := tx.First(task, "id = ?", task.ID).Error; err != nil {
			return err
		}
		return createTaskEvent(tx, task, eventType, level, message)
	})
}

func createTaskEvent(tx *gorm.DB, task *models.Fail2banTask, eventType, level, message string) error {
	result := tx.Model(&models.Fail2banTask{}).Where("id = ?", task.ID).UpdateColumn("event_seq", gorm.Expr("event_seq + 1"))
	if result.Error != nil {
		return result.Error
	}
	if err := tx.Select("event_seq", "status", "phase", "progress").First(task, "id = ?", task.ID).Error; err != nil {
		return err
	}
	return tx.Create(&models.Fail2banTaskEvent{
		TaskID: task.ID, Seq: task.EventSeq, Type: eventType, Level: level,
		Status: task.Status, Phase: task.Phase, Progress: task.Progress, Message: message,
	}).Error
}

func (m *Manager) Get(id string) (*models.Fail2banTask, error) {
	var task models.Fail2banTask
	err := m.db.First(&task, "id = ?", id).Error
	return &task, err
}

func (m *Manager) List(options TaskListOptions) (*TaskList, error) {
	if options.Page < 1 {
		options.Page = 1
	}
	if options.PageSize < 1 || options.PageSize > 100 {
		options.PageSize = 20
	}
	query := m.db.Model(&models.Fail2banTask{})
	if options.ActiveOnly {
		query = query.Where("status IN ?", []string{models.Fail2banTaskQueued, models.Fail2banTaskRunning})
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.Fail2banTask
	if err := query.Order("created_at DESC").Offset((options.Page - 1) * options.PageSize).Limit(options.PageSize).Find(&tasks).Error; err != nil {
		return nil, err
	}
	return &TaskList{Data: tasks, Total: total, Page: options.Page, PageSize: options.PageSize}, nil
}

func (m *Manager) EventsAfter(id string, after int64) ([]models.Fail2banTaskEvent, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	var events []models.Fail2banTaskEvent
	err := m.db.Where("task_id = ? AND seq > ?", id, after).Order("seq ASC").Limit(200).Find(&events).Error
	return events, err
}

func taskErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrValidation):
		return "VALIDATION_FAILED"
	case errors.Is(err, ErrRevisionConflict):
		return "REVISION_CONFLICT"
	case errors.Is(err, ErrProtectedAddress):
		return "PROTECTED_ADDRESS"
	case errors.Is(err, ErrUnavailable):
		return "SERVICE_UNAVAILABLE"
	case errors.Is(err, ErrConfigValidation):
		return "FAIL2BAN_CONFIG_INVALID"
	case errors.Is(err, ErrReload):
		return "FAIL2BAN_RELOAD_FAILED"
	default:
		return "APPLY_FAILED"
	}
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func normalizeTriggeredBy(value string) string {
	if value == "system" {
		return value
	}
	return "user"
}

func TaskResult(task *models.Fail2banTask) map[string]any {
	return map[string]any{
		"taskId": task.ID, "operation": task.Operation, "status": task.Status,
		"statusUrl": fmt.Sprintf("/v1/security/fail2ban/tasks/%s", task.ID),
		"streamUrl": fmt.Sprintf("/v1/security/fail2ban/tasks/%s/events", task.ID),
	}
}
