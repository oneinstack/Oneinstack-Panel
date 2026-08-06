package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"oneinstack/app"
	"oneinstack/internal/models"
)

const containerTaskWorkerCount = 2

type CreateTaskManager struct {
	service *Service
	queue   chan string

	mu       sync.Mutex
	started  bool
	submitMu sync.Mutex
}

func NewCreateTaskManager(service *Service) *CreateTaskManager {
	return &CreateTaskManager{service: service, queue: make(chan string, 64)}
}

func (m *CreateTaskManager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}
	db := app.DB()
	if db == nil {
		return errors.New("database is not initialized")
	}
	var tasks []models.ContainerTask
	if err := db.Where("status IN ?", models.ActiveContainerTaskStatuses()).Find(&tasks).Error; err != nil {
		return fmt.Errorf("load container tasks: %w", err)
	}
	for index := range tasks {
		task := &tasks[index]
		task.Status = models.ContainerTaskStatusQueued
		task.Message = "Panel 重启后重新排队"
		if err := db.Model(task).Updates(map[string]any{
			"status":  task.Status,
			"message": task.Message,
		}).Error; err != nil {
			return fmt.Errorf("requeue container task: %w", err)
		}
	}
	m.started = true
	for i := 0; i < containerTaskWorkerCount; i++ {
		go m.worker()
	}
	for _, task := range tasks {
		m.queue <- task.ID
	}
	return nil
}

func (m *CreateTaskManager) Submit(request ContainerCreateRequest, requestedBy int64) (*models.ContainerTask, error) {
	if m == nil || m.service == nil {
		return nil, errors.New("container task manager is not configured")
	}
	if err := validateContainerCreateRequest(request); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidContainerConfig, err)
	}
	if requestedBy <= 0 {
		return nil, errors.New("authenticated user is required")
	}
	if err := m.Start(); err != nil {
		return nil, err
	}
	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	db := app.DB()
	var active int64
	if err := db.Model(&models.ContainerTask{}).
		Where("name = ? AND status IN ?", request.Name, models.ActiveContainerTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, fmt.Errorf("check active container task: %w", err)
	}
	if active > 0 {
		return nil, fmt.Errorf("容器 %s 已有进行中的创建任务", request.Name)
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode container task request: %w", err)
	}
	now := time.Now().UTC()
	task := &models.ContainerTask{
		ID:          uuid.NewString(),
		Name:        request.Name,
		Image:       request.Image,
		Status:      models.ContainerTaskStatusQueued,
		Message:     "等待拉取镜像",
		RequestedBy: requestedBy,
		RequestJSON: string(requestJSON),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create container task: %w", err)
	}
	select {
	case m.queue <- task.ID:
		return task, nil
	default:
		_ = db.Model(task).Updates(map[string]any{
			"status":        models.ContainerTaskStatusFailed,
			"message":       "容器任务队列已满",
			"error_message": "容器任务队列已满，请稍后重试",
			"finished_at":   time.Now().UTC(),
		})
		return nil, errors.New("container task queue is full")
	}
}

func (m *CreateTaskManager) Get(id string, requestedBy int64) (*models.ContainerTask, error) {
	if id == "" || requestedBy <= 0 {
		return nil, errors.New("invalid container task query")
	}
	db := app.DB()
	if db == nil {
		return nil, errors.New("database is not initialized")
	}
	var task models.ContainerTask
	query := db.Where("id = ?", id)
	if requestedBy > 0 {
		query = query.Where("requested_by = ?", requestedBy)
	}
	if err := query.First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *CreateTaskManager) worker() {
	for taskID := range m.queue {
		m.run(taskID)
	}
}

func (m *CreateTaskManager) run(taskID string) {
	db := app.DB()
	if db == nil {
		return
	}
	var task models.ContainerTask
	if err := db.First(&task, "id = ?", taskID).Error; err != nil || models.IsContainerTaskTerminal(task.Status) {
		return
	}
	var request ContainerCreateRequest
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		m.fail(task.ID, err)
		return
	}
	started := time.Now().UTC()
	m.update(task.ID, map[string]any{
		"status":     models.ContainerTaskStatusPulling,
		"message":    "正在拉取镜像",
		"started_at": started,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := m.service.PullImage(ctx, request.Image); err != nil {
		m.fail(task.ID, err)
		return
	}
	m.update(task.ID, map[string]any{
		"status":  models.ContainerTaskStatusCreating,
		"message": "正在创建容器",
	})
	containerID, err := m.service.CreateContainer(ctx, request)
	if err != nil {
		m.fail(task.ID, err)
		return
	}
	m.update(task.ID, map[string]any{
		"status":       models.ContainerTaskStatusSucceeded,
		"message":      "容器创建成功",
		"container_id": containerID,
		"finished_at":  time.Now().UTC(),
	})
}

func (m *CreateTaskManager) fail(id string, err error) {
	m.update(id, map[string]any{
		"status":        models.ContainerTaskStatusFailed,
		"message":       "容器创建失败",
		"error_message": err.Error(),
		"finished_at":   time.Now().UTC(),
	})
}

func (m *CreateTaskManager) update(id string, values map[string]any) {
	if db := app.DB(); db != nil {
		values["updated_at"] = time.Now().UTC()
		_ = db.Model(&models.ContainerTask{}).Where("id = ?", id).Updates(values).Error
	}
}
