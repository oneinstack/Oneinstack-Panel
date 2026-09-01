package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"oneinstack/app"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
)

const (
	containerTaskWorkerCount = 2
	containerTaskQueueSize   = 128
	containerTaskLogDir      = "logs/container-tasks"
	containerTaskTimeout     = 35 * time.Minute
)

type BuildTaskRequest struct {
	Name           string            `json:"name"`
	Dockerfile     string            `json:"dockerfile,omitempty"`
	ContextPath    string            `json:"contextPath,omitempty"`
	DockerfilePath string            `json:"dockerfilePath,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	LabelsText     string            `json:"labelsText,omitempty"`
}

type TaskRequest struct {
	Operation   string                  `json:"operation"`
	Image       string                  `json:"image,omitempty"`
	ContainerID string                  `json:"containerId,omitempty"`
	Network     string                  `json:"network,omitempty"`
	Create      *ContainerCreateRequest `json:"create,omitempty"`
	Build       *BuildTaskRequest       `json:"build,omitempty"`
	Compose     *ComposeTaskRequest     `json:"compose,omitempty"`
}

type TaskListOptions struct {
	RequestedBy int64
	IncludeAll  bool
	ComposeOnly bool
	ActiveOnly  bool
	Operation   string
	Status      string
	Page        int
	PageSize    int
}

type TaskList struct {
	Data     []models.ContainerTask `json:"data"`
	Total    int64                  `json:"total"`
	Page     int                    `json:"page"`
	PageSize int                    `json:"pageSize"`
}

type TaskLogChunk struct {
	Content    string `json:"content"`
	NextCursor int64  `json:"nextCursor"`
	EOF        bool   `json:"eof"`
}

type CreateTaskManager struct {
	service *Service
	queue   chan string

	mu          sync.Mutex
	started     bool
	submitMu    sync.Mutex
	cancelMu    sync.Mutex
	cancels     map[string]context.CancelFunc
	subscribeMu sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
}

func NewCreateTaskManager(service *Service) *CreateTaskManager {
	return &CreateTaskManager{
		service: service, queue: make(chan string, containerTaskQueueSize),
		cancels: make(map[string]context.CancelFunc), subscribers: make(map[string]map[chan struct{}]struct{}),
	}
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
	now := time.Now().UTC()
	for index := range tasks {
		task := &tasks[index]
		task.Status = models.ContainerTaskStatusInterrupted
		task.Phase = models.ContainerTaskStatusInterrupted
		task.ErrorCode = "PANEL_RESTARTED"
		task.ErrorMessage = "Panel 重启导致容器任务中断"
		task.Message = task.ErrorMessage
		task.FinishedAt = &now
		task.UpdatedAt = now
		task.EventSeq++
		if err := db.Save(task).Error; err != nil {
			return fmt.Errorf("interrupt container task: %w", err)
		}
		var request TaskRequest
		if json.Unmarshal([]byte(task.RequestJSON), &request) == nil {
			cleanupComposeTask(request.Compose)
		}
		appendTaskAudit(task, "interrupted", task.ErrorMessage)
		_ = m.appendEvent(task.ID, "terminal", "error", task.Status, task.Phase, task.Progress, task.Message, nil, "", task.ErrorCode)
	}
	m.started = true
	for i := 0; i < containerTaskWorkerCount; i++ {
		go m.worker()
	}
	return nil
}

func (m *CreateTaskManager) Submit(request TaskRequest, requestedBy int64) (*models.ContainerTask, error) {
	if m == nil || m.service == nil {
		return nil, errors.New("container task manager is not configured")
	}
	if requestedBy <= 0 {
		return nil, errors.New("authenticated user is required")
	}
	if err := m.validateTaskRequest(request); err != nil {
		return nil, err
	}
	if request.Compose != nil && request.Compose.Target.ProjectName == "" {
		request.Compose.Target.ProjectName = request.Compose.ProjectName
	}
	if err := m.Start(); err != nil {
		return nil, err
	}
	m.submitMu.Lock()
	defer m.submitMu.Unlock()
	db := app.DB()
	name, image := request.Image, request.Image
	if request.Create != nil {
		name, image = request.Create.Name, request.Create.Image
	}
	if request.Compose != nil {
		name, image = request.Compose.ProjectName, ""
	}
	if isNetworkOperation(request.Operation) {
		name, image = request.ContainerID, request.Network
	}
	var active int64
	query := db.Model(&models.ContainerTask{}).Where("status IN ?", models.ActiveContainerTaskStatuses())
	if isComposeOperation(request.Operation) {
		query = query.Where("name = ? AND operation LIKE ?", name, "compose.%")
	} else if request.Operation == models.ContainerTaskOperationCreate {
		query = query.Where("name = ?", name)
	} else if isNetworkOperation(request.Operation) {
		query = query.Where("name = ? AND operation LIKE ?", name, "network.%")
	} else {
		query = query.Where("operation = ? AND image = ?", request.Operation, image)
	}
	if err := query.Count(&active).Error; err != nil {
		return nil, fmt.Errorf("check active container task: %w", err)
	}
	if active > 0 {
		if isComposeOperation(request.Operation) {
			return nil, composeError(ErrComposeProjectBusy, "Compose 项目已有进行中的任务")
		}
		return nil, errors.New("相同资源已有进行中的容器任务")
	}
	requestJSON, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode container task request: %w", err)
	}
	now := time.Now().UTC()
	task := &models.ContainerTask{
		ID: uuid.NewString(), Operation: request.Operation, Name: name, Image: image, ContainerID: request.ContainerID, Network: request.Network,
		Status: models.ContainerTaskStatusQueued, Phase: models.ContainerTaskStatusQueued,
		Message: "任务已进入队列", RequestedBy: requestedBy, RequestJSON: string(requestJSON),
		EventSeq: 1, CreatedAt: now, UpdatedAt: now,
		LogPath: filepath.Join(app.GetBasePath(), containerTaskLogDir, "task_"+uuid.NewString()+".log"),
	}
	if err := db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create container task: %w", err)
	}
	if err := m.appendEvent(task.ID, "snapshot", "info", task.Status, task.Phase, 0, task.Message, nil, "", "task_queued"); err != nil {
		return nil, err
	}
	select {
	case m.queue <- task.ID:
		return task, nil
	default:
		_ = m.finish(task.ID, models.ContainerTaskStatusFailed, "QUEUE_FULL", "容器任务队列已满，请稍后重试")
		cleanupComposeTask(request.Compose)
		return nil, errors.New("container task queue is full")
	}
}

func (m *CreateTaskManager) validateTaskRequest(request TaskRequest) error {
	switch request.Operation {
	case models.ContainerTaskOperationPull:
		if _, err := validateReference(request.Image); err != nil {
			return err
		}
	case models.ContainerTaskOperationBuild:
		if request.Build == nil {
			return errors.New("构建参数不能为空")
		}
		if request.Build.Name == "" {
			return errors.New("镜像名称不能为空")
		}
	case models.ContainerTaskOperationCreate:
		if request.Create == nil {
			return errors.New("容器创建参数不能为空")
		}
		if err := validateContainerCreateRequest(*request.Create); err != nil {
			return err
		}
	case models.ContainerTaskOperationNetworkConnect,
		models.ContainerTaskOperationNetworkDisconnect,
		models.ContainerTaskOperationNetworkReconnect:
		if _, err := validateReference(request.ContainerID); err != nil {
			return fmt.Errorf("容器标识无效: %w", err)
		}
		if _, err := validateName(request.Network); err != nil {
			return fmt.Errorf("网络名称无效: %w", err)
		}
	case models.ContainerTaskOperationComposeCreate,
		models.ContainerTaskOperationComposeEdit,
		models.ContainerTaskOperationComposeStart,
		models.ContainerTaskOperationComposeStop,
		models.ContainerTaskOperationComposeRestart,
		models.ContainerTaskOperationComposeUpdate,
		models.ContainerTaskOperationComposeDelete:
		if request.Compose == nil {
			return composeError(ErrComposeConfigInvalid, "Compose 任务参数不能为空")
		}
		if _, err := validateComposeProjectName(request.Compose.ProjectName); err != nil {
			return err
		}
		if request.Compose.Target.ProjectName != "" && request.Compose.Target.ProjectName != request.Compose.ProjectName {
			return composeError(ErrComposeConfigInvalid, "Compose 任务目标与项目名称不一致")
		}
		if (request.Operation == models.ContainerTaskOperationComposeCreate || request.Operation == models.ContainerTaskOperationComposeEdit) && request.Compose.ContentPath == "" {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置内容缺失")
		}
	default:
		return fmt.Errorf("不支持的容器任务操作: %s", request.Operation)
	}
	return nil
}

func (m *CreateTaskManager) Get(id string, requestedBy int64, includeAll bool) (*models.ContainerTask, error) {
	if id == "" || requestedBy <= 0 {
		return nil, errors.New("invalid container task query")
	}
	db := app.DB()
	if db == nil {
		return nil, errors.New("database is not initialized")
	}
	query := db.Where("id = ?", id)
	if !includeAll {
		query = query.Where("requested_by = ?", requestedBy)
	}
	var task models.ContainerTask
	if err := query.First(&task).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *CreateTaskManager) List(options TaskListOptions) (*TaskList, error) {
	if options.Page < 1 {
		options.Page = 1
	}
	if options.PageSize < 1 || options.PageSize > 100 {
		options.PageSize = 20
	}
	db := app.DB()
	query := db.Model(&models.ContainerTask{})
	if !options.IncludeAll {
		query = query.Where("requested_by = ?", options.RequestedBy)
	}
	if options.ComposeOnly {
		query = query.Where("operation LIKE ?", "compose.%")
	}
	if options.ActiveOnly {
		query = query.Where("status IN ?", models.ActiveContainerTaskStatuses())
	}
	if options.Operation != "" {
		query = query.Where("operation = ?", options.Operation)
	}
	if options.Status != "" {
		query = query.Where("status = ?", options.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.ContainerTask
	if err := query.Order("created_at DESC").Offset((options.Page - 1) * options.PageSize).Limit(options.PageSize).Find(&tasks).Error; err != nil {
		return nil, err
	}
	return &TaskList{Data: tasks, Total: total, Page: options.Page, PageSize: options.PageSize}, nil
}

func (m *CreateTaskManager) EventsAfter(taskID string, after int64, limit int) ([]models.ContainerTaskEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var events []models.ContainerTaskEvent
	err := app.DB().Where("task_id = ? AND seq > ?", taskID, after).Order("seq ASC").Limit(limit).Find(&events).Error
	return events, err
}

func (m *CreateTaskManager) ReadLog(taskID string, cursor, limit int64, requestedBy int64, includeAll bool) (*TaskLogChunk, error) {
	task, err := m.Get(taskID, requestedBy, includeAll)
	if err != nil {
		return nil, err
	}
	if cursor < 0 {
		return nil, errors.New("log cursor cannot be negative")
	}
	if limit <= 0 || limit > 64*1024 {
		limit = 64 * 1024
	}
	path, err := m.safeLogPath(task)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &TaskLogChunk{NextCursor: cursor, EOF: models.IsContainerTaskTerminal(task.Status)}, nil
	}
	if err != nil {
		return nil, err
	}
	if cursor > int64(len(data)) {
		return nil, errors.New("log cursor exceeds current log size")
	}
	end := cursor + limit
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return &TaskLogChunk{Content: string(data[cursor:end]), NextCursor: end, EOF: end >= int64(len(data)) && models.IsContainerTaskTerminal(task.Status)}, nil
}

func (m *CreateTaskManager) OpenLog(taskID string, requestedBy int64, includeAll bool) (*os.File, os.FileInfo, error) {
	task, err := m.Get(taskID, requestedBy, includeAll)
	if err != nil {
		return nil, nil, err
	}
	path, err := m.safeLogPath(task)
	if err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, errors.New("容器任务日志不是普通文件")
	}
	return file, info, nil
}

func (m *CreateTaskManager) Cancel(taskID string, requestedBy int64, includeAll bool) (*models.ContainerTask, error) {
	task, err := m.Get(taskID, requestedBy, includeAll)
	if err != nil {
		return nil, err
	}
	if models.IsContainerTaskTerminal(task.Status) {
		return task, nil
	}
	_ = app.DB().Model(&models.ContainerTask{}).Where("id = ? AND status IN ?", taskID, models.ActiveContainerTaskStatuses()).Updates(map[string]any{
		"status": models.ContainerTaskStatusCanceling, "phase": models.ContainerTaskStatusCanceling,
		"message": "正在取消容器任务", "cancel_requested": true, "updated_at": time.Now().UTC(),
	}).Error
	m.cancelMu.Lock()
	if cancel := m.cancels[taskID]; cancel != nil {
		cancel()
	}
	m.cancelMu.Unlock()
	return m.Get(taskID, requestedBy, includeAll)
}

func (m *CreateTaskManager) Subscribe(taskID string) (chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	m.subscribeMu.Lock()
	if m.subscribers[taskID] == nil {
		m.subscribers[taskID] = make(map[chan struct{}]struct{})
	}
	m.subscribers[taskID][channel] = struct{}{}
	m.subscribeMu.Unlock()
	return channel, func() {
		m.subscribeMu.Lock()
		delete(m.subscribers[taskID], channel)
		close(channel)
		m.subscribeMu.Unlock()
	}
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
	if task.CancelRequested || task.Status == models.ContainerTaskStatusCanceling {
		_ = m.finish(task.ID, models.ContainerTaskStatusCanceled, "ACTION_CANCELED", "容器任务已取消")
		return
	}
	var request TaskRequest
	if err := json.Unmarshal([]byte(task.RequestJSON), &request); err != nil {
		m.fail(task.ID, "INVALID_REQUEST", err.Error())
		return
	}
	// Pull/build commands have their own 30-minute ceiling. Keep a separate
	// margin for the create and verify phases so a successful slow pull is not
	// canceled immediately by the task-level deadline.
	ctx, cancel := context.WithTimeout(context.Background(), containerTaskTimeout)
	m.cancelMu.Lock()
	m.cancels[task.ID] = cancel
	m.cancelMu.Unlock()
	defer func() { cancel(); m.cancelMu.Lock(); delete(m.cancels, task.ID); m.cancelMu.Unlock() }()
	started := time.Now().UTC()
	m.update(task.ID, map[string]any{"started_at": started, "heartbeat_at": started})
	emit := m.lineEmitter(task.ID, request.Operation)
	if isComposeOperation(request.Operation) {
		emit = m.composeLineEmitter(task.ID, request.Operation)
	}
	var err error
	if isComposeOperation(request.Operation) && request.Compose != nil {
		err = m.service.ValidateComposeTaskPreview(ctx, request.Operation, *request.Compose)
	}
	switch request.Operation {
	case models.ContainerTaskOperationPull:
		m.phase(task.ID, models.ContainerTaskStatusPulling, 5, "正在拉取镜像")
		err = m.service.PullImageStream(ctx, request.Image, emit)
	case models.ContainerTaskOperationBuild:
		m.phase(task.ID, models.ContainerTaskStatusBuilding, 5, "正在构建镜像")
		b := request.Build
		err = m.service.BuildImageStream(ctx, b.Name, b.Dockerfile, b.ContextPath, b.DockerfilePath, b.Labels, b.LabelsText, emit)
	case models.ContainerTaskOperationCreate:
		m.phase(task.ID, models.ContainerTaskStatusResolving, 3, "正在检查镜像")
		var containerID string
		available, checkErr := m.service.ImageAvailable(ctx, request.Create.Image)
		if checkErr != nil {
			err = checkErr
		} else {
			if !available {
				m.phase(task.ID, models.ContainerTaskStatusPulling, 25, "镜像不存在，正在拉取")
				err = m.service.PullImageStream(ctx, request.Create.Image, emit)
			}
			if err == nil {
				m.phase(task.ID, models.ContainerTaskStatusCreating, 75, "正在创建容器")
				containerID, err = m.service.CreateContainer(ctx, *request.Create)
				if err == nil {
					m.update(task.ID, map[string]any{"container_id": containerID})
				}
			}
			if err == nil {
				m.phase(task.ID, models.ContainerTaskStatusVerifying, 95, "正在验证容器")
				if len(request.Create.Networks) > 0 {
					err = m.service.VerifyContainerNetworks(ctx, containerID, request.Create.Networks)
				}
			}
		}
	case models.ContainerTaskOperationNetworkConnect,
		models.ContainerTaskOperationNetworkDisconnect,
		models.ContainerTaskOperationNetworkReconnect:
		m.phase(task.ID, models.ContainerTaskStatusCreating, 20, "正在调整容器网络")
		err = m.service.ContainerNetworkAction(ctx, request.ContainerID, request.Network, networkOperationAction(request.Operation))
	case models.ContainerTaskOperationComposeCreate,
		models.ContainerTaskOperationComposeEdit,
		models.ContainerTaskOperationComposeStart,
		models.ContainerTaskOperationComposeStop,
		models.ContainerTaskOperationComposeRestart,
		models.ContainerTaskOperationComposeUpdate,
		models.ContainerTaskOperationComposeDelete:
		m.phase(task.ID, models.ContainerTaskStatusResolving, 5, "正在准备 Compose 项目")
		if err == nil {
			switch request.Operation {
			case models.ContainerTaskOperationComposeCreate:
				m.phase(task.ID, models.ContainerTaskStatusCreating, 20, "正在创建 Compose 项目")
			case models.ContainerTaskOperationComposeEdit:
				m.composePhase(task.ID, models.ContainerTaskStatusCreating, "editing", 50, "正在保存 Compose 配置")
			case models.ContainerTaskOperationComposeUpdate:
				m.composePhase(task.ID, models.ContainerTaskStatusPulling, "updating", 20, "正在拉取 Compose 镜像")
			case models.ContainerTaskOperationComposeStart, models.ContainerTaskOperationComposeRestart:
				phase := "starting"
				message := "正在启动 Compose 服务"
				if request.Operation == models.ContainerTaskOperationComposeRestart {
					phase = "restarting"
					message = "正在重启 Compose 服务"
				}
				m.composePhase(task.ID, models.ContainerTaskStatusCreating, phase, 20, message)
			case models.ContainerTaskOperationComposeStop:
				m.composePhase(task.ID, models.ContainerTaskStatusCreating, "stopping", 20, "正在停止 Compose 服务")
			case models.ContainerTaskOperationComposeDelete:
				m.composePhase(task.ID, models.ContainerTaskStatusCreating, "deleting", 20, "正在删除 Compose 资源")
			}
		}
		if err == nil && request.Compose == nil {
			err = composeError(ErrComposeConfigInvalid, "Compose 任务参数缺失")
		} else if err == nil {
			err = m.service.RunComposeTask(ctx, request.Operation, *request.Compose, emit)
		}
	}
	cleanupComposeTask(request.Compose)
	if err != nil {
		if m.isCancelRequested(task.ID) || errors.Is(ctx.Err(), context.Canceled) {
			m.finish(task.ID, models.ContainerTaskStatusCanceled, "ACTION_CANCELED", "容器任务已取消")
		} else if errors.Is(err, ErrDockerCommandTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			m.fail(task.ID, "DOCKER_OPERATION_TIMEOUT", "Docker 操作超时，请检查测试环境 Docker daemon 的 DNS、代理或镜像加速配置后重试")
		} else if isComposeOperation(request.Operation) {
			code, message := composeTaskFailure(err)
			m.fail(task.ID, code, message)
		} else {
			code := "DOCKER_OPERATION_FAILED"
			switch {
			case errors.Is(err, ErrContainerNetworkAttachmentFailed):
				code = "CONTAINER_NETWORK_ATTACHMENT_FAILED"
			case errors.Is(err, ErrContainerNetworkAlreadyAttached):
				code = "CONTAINER_NETWORK_ALREADY_ATTACHED"
			case errors.Is(err, ErrContainerNetworkNotAttached):
				code = "CONTAINER_NETWORK_NOT_ATTACHED"
			case isNetworkOperation(request.Operation):
				code = "DOCKER_NETWORK_OPERATION_FAILED"
			}
			m.fail(task.ID, code, dockerDiagnosticSummary(err.Error()))
		}
		return
	}
	switch {
	case request.Operation == models.ContainerTaskOperationCreate:
		m.finish(task.ID, models.ContainerTaskStatusSucceeded, "CONTAINER_CREATED", "容器创建成功，尚未启动")
	case isNetworkOperation(request.Operation):
		m.finish(task.ID, models.ContainerTaskStatusSucceeded, "CONTAINER_NETWORK_UPDATED", "容器网络调整成功")
	case isComposeOperation(request.Operation):
		code, message := composeTaskSuccess(request.Operation)
		m.finish(task.ID, models.ContainerTaskStatusSucceeded, code, message)
	default:
		m.finish(task.ID, models.ContainerTaskStatusSucceeded, "", "容器任务执行成功")
	}
}

func (m *CreateTaskManager) composeLineEmitter(taskID, operation string) func(string) {
	return func(line string) {
		m.lineEmitter(taskID, operation)(redactContainerLogLine(line))
	}
}

func composeTaskFailure(err error) (string, string) {
	switch {
	case errors.Is(err, ErrComposeConfigInvalid):
		return "COMPOSE_CONFIG_INVALID", "Compose 配置校验失败"
	case errors.Is(err, ErrComposeProjectNotFound):
		return "COMPOSE_PROJECT_NOT_FOUND", "Compose 项目不存在"
	case errors.Is(err, ErrComposeProjectConflict):
		return "COMPOSE_PROJECT_CONFLICT", "Compose 项目已存在或状态冲突"
	case errors.Is(err, ErrComposeProjectBusy):
		return "COMPOSE_PROJECT_BUSY", "Compose 项目已有进行中的任务"
	case errors.Is(err, ErrComposePreviewStale):
		return "COMPOSE_PREVIEW_STALE", "Compose 预览已失效，请重新预览"
	case errors.Is(err, ErrComposeMultiFile):
		return "COMPOSE_MULTI_FILE_EDIT_UNSUPPORTED", "当前 Compose 项目使用多个配置文件，暂不支持编辑"
	case errors.Is(err, ErrComposeOperationTimeout):
		return "COMPOSE_OPERATION_TIMEOUT", "Docker Compose 操作超时"
	case errors.Is(err, ErrComposeUnavailable):
		return "COMPOSE_UNAVAILABLE", "Docker Compose 插件不可用"
	case errors.Is(err, ErrRuntimeUnavailable):
		return "DOCKER_RUNTIME_UNAVAILABLE", "Docker 运行时不可用"
	default:
		return "COMPOSE_OPERATION_FAILED", "Docker Compose 操作失败，请查看任务日志中的脱敏输出"
	}
}

func composeTaskSuccess(operation string) (string, string) {
	switch operation {
	case models.ContainerTaskOperationComposeCreate:
		return "COMPOSE_CREATED", "Compose 项目创建并启动成功"
	case models.ContainerTaskOperationComposeEdit:
		return "COMPOSE_CONFIG_SAVED", "Compose 配置保存成功"
	case models.ContainerTaskOperationComposeStart:
		return "COMPOSE_STARTED", "Compose 服务启动成功"
	case models.ContainerTaskOperationComposeStop:
		return "COMPOSE_STOPPED", "Compose 服务停止成功"
	case models.ContainerTaskOperationComposeRestart:
		return "COMPOSE_RESTARTED", "Compose 服务重启成功"
	case models.ContainerTaskOperationComposeUpdate:
		return "COMPOSE_UPDATED", "Compose 项目更新成功"
	case models.ContainerTaskOperationComposeDelete:
		return "COMPOSE_DELETED", "Compose 项目删除成功"
	default:
		return "", "容器任务执行成功"
	}
}

func (m *CreateTaskManager) lineEmitter(taskID, operation string) func(string) {
	return func(line string) {
		line = strings.TrimSpace(line)
		if line == "" {
			return
		}
		m.appendLog(taskID, line+"\n")
		progress, phaseProgress, message, details := parseDockerProgress(line, operation)
		if message == "" {
			message = line
		}
		if progress == 0 {
			var task models.ContainerTask
			if app.DB().Select("progress").First(&task, "id = ?", taskID).Error == nil && task.Progress > 0 {
				progress = task.Progress
			}
		}
		m.updateEvent(taskID, "progress", "info", progress, phaseProgress, message, details, line, "")
	}
}

var buildStepPattern = regexp.MustCompile(`(?i)(?:step|步骤)\s+(\d+)\s*/\s*(\d+)`)

func parseDockerProgress(line, operation string) (int, *int, string, []map[string]any) {
	details := make([]map[string]any, 0)
	if operation == models.ContainerTaskOperationBuild {
		matches := buildStepPattern.FindStringSubmatch(line)
		if len(matches) == 3 {
			current, _ := strconv.Atoi(matches[1])
			total, _ := strconv.Atoi(matches[2])
			if total > 0 {
				p := current * 100 / total
				return p, &p, line, details
			}
		}
		return 0, nil, line, details
	}
	var item struct {
		Status         string `json:"status"`
		ID             string `json:"id"`
		ProgressDetail struct {
			Current int64 `json:"current"`
			Total   int64 `json:"total"`
		} `json:"progressDetail"`
	}
	if json.Unmarshal([]byte(line), &item) == nil && item.Status != "" {
		if item.ID != "" {
			details = append(details, map[string]any{"id": item.ID, "status": item.Status, "current": item.ProgressDetail.Current, "total": item.ProgressDetail.Total})
		}
		if item.ProgressDetail.Total > 0 {
			progress := int(item.ProgressDetail.Current * 100 / item.ProgressDetail.Total)
			if progress > 100 {
				progress = 100
			}
			return progress, &progress, item.Status, details
		}
		return 0, nil, item.Status, details
	}
	return 0, nil, line, details
}

func (m *CreateTaskManager) phase(id, status string, progress int, message string) {
	m.updateEvent(id, "phase", "info", progress, nil, message, nil, "", "", map[string]any{"status": status, "phase": status})
}

func (m *CreateTaskManager) composePhase(id, status, phase string, progress int, message string) {
	m.updateEvent(id, "phase", "info", progress, nil, message, nil, "", "", map[string]any{"status": status, "phase": phase})
}

func (m *CreateTaskManager) finish(id, status, code, message string) error {
	err := m.updateEvent(id, "terminal", "info", 100, nil, message, nil, "", code, map[string]any{"status": status, "finished_at": time.Now().UTC()})
	m.auditTask(id, status, message)
	return err
}

func (m *CreateTaskManager) fail(id, code, message string) {
	_ = m.updateEvent(id, "terminal", "error", 100, nil, "容器任务失败", nil, "", code, map[string]any{"status": models.ContainerTaskStatusFailed, "error_code": code, "error_message": message, "finished_at": time.Now().UTC()})
	m.auditTask(id, models.ContainerTaskStatusFailed, message)
}

func (m *CreateTaskManager) auditTask(id, status, message string) {
	var task models.ContainerTask
	if app.DB().Select("id", "operation", "requested_by").First(&task, "id = ?", id).Error != nil {
		return
	}
	appendTaskAudit(&task, status, message)
}

func appendTaskAudit(task *models.ContainerTask, status, message string) {
	manager := auditservice.Default()
	if manager == nil || task == nil {
		return
	}
	_, _ = manager.Append(auditservice.EventInput{EventType: "container", Action: "container.task." + task.Operation + "." + status, Method: "WORKER", Route: "/v1/containers/tasks/" + task.ID, Path: "/v1/containers/tasks/" + task.ID, Status: 200, Outcome: status, UserID: task.RequestedBy, Message: strings.TrimSpace(message)})
}

func (m *CreateTaskManager) isCancelRequested(id string) bool {
	var task models.ContainerTask
	return app.DB().Select("cancel_requested").First(&task, "id = ?", id).Error == nil && task.CancelRequested
}

func (m *CreateTaskManager) update(id string, values map[string]any) {
	values["updated_at"] = time.Now().UTC()
	_ = app.DB().Model(&models.ContainerTask{}).Where("id = ?", id).Updates(values).Error
}

func (m *CreateTaskManager) updateEvent(id, typ, level string, progress int, phaseProgress *int, message string, details []map[string]any, log, code string, extra ...map[string]any) error {
	var task models.ContainerTask
	if err := app.DB().First(&task, "id = ?", id).Error; err != nil {
		return err
	}
	status := task.Status
	phase := task.Phase
	if typ == "phase" {
		status, phase = messageToStatus(message), messageToStatus(message)
	}
	if len(extra) > 0 {
		if value, ok := extra[0]["status"].(string); ok {
			status = value
		}
		if value, ok := extra[0]["error_message"].(string); ok {
			message = value
		}
		if value, ok := extra[0]["phase"].(string); ok {
			phase = value
		}
	}
	task.EventSeq++
	task.Progress = progress
	task.PhaseProgress = phaseProgress
	task.Message = message
	task.Status = status
	task.Phase = phase
	task.UpdatedAt = time.Now().UTC()
	task.HeartbeatAt = &task.UpdatedAt
	if value, ok := extraValue(extra, "error_code"); ok {
		if text, ok := value.(string); ok {
			task.ErrorCode = text
		}
	}
	if value, ok := extraValue(extra, "error_message"); ok {
		if text, ok := value.(string); ok {
			task.ErrorMessage = text
		}
	}
	if value, ok := extraValue(extra, "finished_at"); ok {
		if t, ok := value.(time.Time); ok {
			task.FinishedAt = &t
		}
	}
	if err := app.DB().Save(&task).Error; err != nil {
		return err
	}
	detailsJSON := ""
	if len(details) > 0 {
		data, _ := json.Marshal(details)
		detailsJSON = string(data)
	}
	event := &models.ContainerTaskEvent{TaskID: id, Seq: task.EventSeq, Type: typ, Level: level, Status: status, Phase: phase, PhaseProgress: phaseProgress, Progress: progress, Message: message, DetailsJSON: detailsJSON, Log: log, Code: code, CreatedAt: time.Now().UTC()}
	if err := app.DB().Create(event).Error; err != nil {
		return err
	}
	m.notify(id)
	return nil
}

func (m *CreateTaskManager) appendEvent(id, typ, level, status, phase string, progress int, message string, details []map[string]any, log, code string) error {
	return m.updateEvent(id, typ, level, progress, nil, message, details, log, code, map[string]any{"status": status})
}

func (m *CreateTaskManager) appendLog(id, content string) {
	var task models.ContainerTask
	if app.DB().Select("log_path").First(&task, "id = ?", id).Error != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(task.LogPath), 0750); err != nil {
		return
	}
	file, err := os.OpenFile(task.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err == nil {
		_, _ = file.WriteString(content)
		_ = file.Close()
	}
}

func (m *CreateTaskManager) safeLogPath(task *models.ContainerTask) (string, error) {
	base := filepath.Join(app.GetBasePath(), containerTaskLogDir)
	path := filepath.Clean(task.LogPath)
	rel, err := filepath.Rel(base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("容器任务日志路径无效")
	}
	return path, nil
}

func (m *CreateTaskManager) notify(id string) {
	m.subscribeMu.Lock()
	defer m.subscribeMu.Unlock()
	for channel := range m.subscribers[id] {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

func extraValue(extra []map[string]any, key string) (any, bool) {
	if len(extra) == 0 {
		return nil, false
	}
	value, ok := extra[0][key]
	return value, ok
}

func messageToStatus(message string) string {
	switch {
	case strings.Contains(message, "拉取"):
		return models.ContainerTaskStatusPulling
	case strings.Contains(message, "构建"):
		return models.ContainerTaskStatusBuilding
	case strings.Contains(message, "创建"):
		return models.ContainerTaskStatusCreating
	case strings.Contains(message, "验证"):
		return models.ContainerTaskStatusVerifying
	case strings.Contains(message, "启动"), strings.Contains(message, "停止"), strings.Contains(message, "保存"), strings.Contains(message, "删除"):
		return models.ContainerTaskStatusCreating
	default:
		return models.ContainerTaskStatusResolving
	}
}

func isNetworkOperation(operation string) bool {
	switch operation {
	case models.ContainerTaskOperationNetworkConnect,
		models.ContainerTaskOperationNetworkDisconnect,
		models.ContainerTaskOperationNetworkReconnect:
		return true
	default:
		return false
	}
}

func networkOperationAction(operation string) string {
	switch operation {
	case models.ContainerTaskOperationNetworkConnect:
		return "connect"
	case models.ContainerTaskOperationNetworkDisconnect:
		return "disconnect"
	case models.ContainerTaskOperationNetworkReconnect:
		return "reconnect"
	default:
		return ""
	}
}
