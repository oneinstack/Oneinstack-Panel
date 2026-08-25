package softwaretask

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	defaultQueueSize  = 128
	defaultWorkerSize = 2
)

type InstallRequest struct {
	Operation             string
	Key                   string
	Version               string
	Port                  string
	Username              string
	Password              string
	Parameters            map[string]string
	Revision              string
	PreviousConfiguration map[string]string
	Configuration         map[string]string
	RestoreFromID         string
	SwitchRequested       bool
}

type Executor func(
	ctx context.Context,
	request InstallRequest,
	logPath string,
	reporter *Reporter,
) error

type RecoveryInspection struct {
	Status  string
	Message string
}

type RecoveryInspector func(context.Context, *models.SoftwareTask) RecoveryInspection

type RuntimeGroupOwner struct {
	Component   string
	ServiceName string
}

type queuedTask struct {
	taskID  string
	request InstallRequest
}

type Manager struct {
	db        *gorm.DB
	logDir    string
	executor  Executor
	inspector RecoveryInspector
	queue     chan queuedTask

	startOnce sync.Once
	startErr  error
	submitMu  sync.Mutex
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc

	subscribeMu sync.Mutex
	subscribers map[string]map[chan struct{}]struct{}
	eventLocks  sync.Map
	lifecycleMu sync.Mutex
	runWG       sync.WaitGroup
	stopping    atomic.Bool
}

// SetRecoveryInspector configures a read-only component health probe used
// before interrupted tasks are finalized during Panel startup.
func (m *Manager) SetRecoveryInspector(inspector RecoveryInspector) {
	m.inspector = inspector
}

func NewManager(db *gorm.DB, logDir string, executor Executor) *Manager {
	return &Manager{
		db:          db,
		logDir:      filepath.Clean(logDir),
		executor:    executor,
		queue:       make(chan queuedTask, defaultQueueSize),
		cancels:     make(map[string]context.CancelFunc),
		subscribers: make(map[string]map[chan struct{}]struct{}),
	}
}

func (m *Manager) Start() error {
	m.startOnce.Do(func() {
		if m.db == nil {
			m.startErr = errors.New("software task database is not initialized")
			return
		}
		if m.executor == nil {
			m.startErr = errors.New("software task executor is not configured")
			return
		}
		if m.logDir == "." || m.logDir == string(filepath.Separator) {
			m.startErr = errors.New("software task log directory is invalid")
			return
		}
		if err := os.MkdirAll(m.logDir, 0750); err != nil {
			m.startErr = fmt.Errorf("create software task log directory: %w", err)
			return
		}
		// Keep the runtime lock usable for standalone managers as well as the
		// application-wide migration path. This also makes Panel restart
		// recovery safe when the manager is initialized before the main DB
		// migration completes.
		if err := m.db.AutoMigrate(&models.RuntimeGroupOperationLock{}); err != nil {
			m.startErr = fmt.Errorf("migrate runtime group task locks: %w", err)
			return
		}
		if err := m.reconcileInterruptedTasks(); err != nil {
			m.startErr = err
			return
		}
		for i := 0; i < defaultWorkerSize; i++ {
			go m.worker()
		}
	})
	return m.startErr
}

func (m *Manager) Submit(request InstallRequest, requestedBy int64) (*models.SoftwareTask, error) {
	request.Operation = "install"
	return m.submit(request, requestedBy)
}

func (m *Manager) SubmitUninstall(name, version string, requestedBy int64) (*models.SoftwareTask, error) {
	return m.SubmitUninstallWithParameters(name, version, nil, requestedBy)
}

func (m *Manager) SubmitUninstallWithParameters(name, version string, parameters map[string]string, requestedBy int64) (*models.SoftwareTask, error) {
	key, err := m.softwareKeyForUninstall(name)
	if err != nil {
		return nil, err
	}
	return m.submit(InstallRequest{
		Operation:  "uninstall",
		Key:        key,
		Version:    version,
		Parameters: parameters,
	}, requestedBy)
}

func (m *Manager) SubmitServiceAction(
	component string,
	action string,
	requestedBy int64,
) (*models.SoftwareTask, error) {
	return m.SubmitServiceActionWithSwitch(component, action, false, requestedBy)
}

func (m *Manager) SubmitServiceActionWithSwitch(
	component string,
	action string,
	switchRequested bool,
	requestedBy int64,
) (*models.SoftwareTask, error) {
	key, err := m.softwareKeyForService(component)
	if err != nil {
		return nil, err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if !isServiceOperation(action) {
		return nil, fmt.Errorf("unsupported service action: %s", action)
	}
	return m.submit(InstallRequest{
		Operation:       action,
		Key:             key,
		SwitchRequested: switchRequested,
	}, requestedBy)
}

func (m *Manager) SubmitConfiguration(
	component string,
	revision string,
	previousValues map[string]string,
	values map[string]string,
	restoreFromID string,
	requestedBy int64,
) (*models.SoftwareTask, error) {
	key, err := m.softwareKeyForService(component)
	if err != nil {
		return nil, err
	}
	return m.submit(InstallRequest{
		Operation:             "configure",
		Key:                   key,
		Revision:              strings.TrimSpace(revision),
		PreviousConfiguration: cloneConfigurationValues(previousValues),
		Configuration:         cloneConfigurationValues(values),
		RestoreFromID:         strings.TrimSpace(restoreFromID),
	}, requestedBy)
}

func (m *Manager) submit(request InstallRequest, requestedBy int64) (*models.SoftwareTask, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	if m.stopping.Load() {
		return nil, errors.New("software task manager is stopping")
	}
	request.Key = strings.TrimSpace(request.Key)
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	if request.Operation == "" {
		request.Operation = "install"
	}
	request.Version = strings.TrimSpace(request.Version)
	request.Port = strings.TrimSpace(request.Port)
	request.Username = strings.TrimSpace(request.Username)
	if request.Operation != "install" && request.Operation != "uninstall" &&
		!isRuntimeOperation(request.Operation) {
		return nil, fmt.Errorf("unsupported software task operation: %s", request.Operation)
	}
	if request.Operation == "configure" {
		if err := validateConfigurationTaskPayload(request.Revision, request.Configuration); err != nil {
			return nil, err
		}
		if err := validateConfigurationTaskPayload(request.Revision, request.PreviousConfiguration); err != nil {
			return nil, fmt.Errorf("previous configuration is invalid: %w", err)
		}
		if !sameConfigurationKeys(request.PreviousConfiguration, request.Configuration) {
			return nil, errors.New("previous and target configuration fields must match")
		}
		if request.RestoreFromID != "" {
			if _, err := uuid.Parse(request.RestoreFromID); err != nil {
				return nil, errors.New("restore source ID is invalid")
			}
		}
	} else if request.Revision != "" ||
		len(request.PreviousConfiguration) != 0 ||
		len(request.Configuration) != 0 ||
		request.RestoreFromID != "" {
		return nil, errors.New("configuration payload is only valid for configure tasks")
	}
	if request.Key == "" {
		return nil, errors.New("software key and version are required")
	}
	if requestedBy <= 0 {
		return nil, errors.New("authenticated user is required")
	}
	component, err := m.componentForKey(request.Key)
	if err != nil {
		return nil, err
	}
	runtimeGroup := m.runtimeGroupForComponent(component)
	if request.SwitchRequested && runtimeGroup == "" {
		return nil, errors.New("SWITCH_UNSUPPORTED: component does not belong to a runtime group")
	}
	if request.SwitchRequested && request.Operation != "start" && request.Operation != "restart" {
		return nil, errors.New("SWITCH_UNSUPPORTED: switch is only valid for start or restart")
	}
	if isRuntimeOperation(request.Operation) && runtimeGroup != "" &&
		(request.Operation == "start" || request.Operation == "restart") && !request.SwitchRequested {
		owners := activeRuntimeGroupOwners(context.Background(), runtimeGroup, component)
		if len(owners) > 0 {
			return nil, fmt.Errorf("RUNTIME_GROUP_BUSY: %s is already active", owners[0].ServiceName)
		}
	}
	if request.Operation == "install" {
		if err := m.validateCatalogInstall(request.Key, request.Version); err != nil {
			return nil, err
		}
		if err := m.validateExclusiveDatabaseInstall(component); err != nil {
			return nil, err
		}
	}

	m.submitMu.Lock()
	defer m.submitMu.Unlock()

	var active int64
	if err := m.db.Model(&models.SoftwareTask{}).
		Where("component = ? AND status IN ?", component, models.ActiveSoftwareTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, fmt.Errorf("check active software task: %w", err)
	}
	if active > 0 {
		return nil, fmt.Errorf("component %s already has an active task", component)
	}

	operation := request.Operation
	var installed models.Software
	installedResult := m.db.
		Where("`key` = ? AND installed = ?", request.Key, true).
		First(&installed)
	if installedResult.Error != nil && !errors.Is(installedResult.Error, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("check installed software: %w", installedResult.Error)
	}
	if request.Operation == "uninstall" || isRuntimeOperation(request.Operation) {
		if errors.Is(installedResult.Error, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("software %s is not installed", component)
		}
		installedVersion := strings.TrimSpace(installed.InstallVersion)
		if installedVersion == "" {
			installedVersion = strings.TrimSpace(installed.Version)
		}
		if request.Version == "" {
			request.Version = installedVersion
		}
		if isRuntimeOperation(request.Operation) && request.Version == "" {
			return nil, fmt.Errorf("installed %s version is missing", component)
		}
		if installedVersion != "" && request.Version != installedVersion {
			return nil, fmt.Errorf(
				"installed %s version is %s, not %s",
				component,
				installedVersion,
				request.Version,
			)
		}
	} else {
		if request.Version == "" {
			return nil, errors.New("software key and version are required")
		}
		if installedResult.Error == nil {
			operation = "upgrade"
		}
	}

	safeParameterValues := map[string]any{
		"port":     request.Port,
		"username": request.Username,
		"switch":   request.SwitchRequested,
	}
	if request.Operation == "configure" {
		safeParameterValues["revision"] = request.Revision
		safeParameterValues["configuration"] = request.Configuration
		if request.RestoreFromID != "" {
			safeParameterValues["restoreFromId"] = request.RestoreFromID
		}
	}
	safeParameters, err := json.Marshal(safeParameterValues)
	if err != nil {
		return nil, fmt.Errorf("encode software task parameters: %w", err)
	}
	taskID := uuid.NewString()
	now := time.Now()
	queuedMessage := "任务已进入" + operationLabel(operation) + "队列"
	task := &models.SoftwareTask{
		ID:               taskID,
		Operation:        operation,
		Component:        component,
		SwitchRequested:  request.SwitchRequested,
		SoftwareKey:      request.Key,
		RequestedVersion: request.Version,
		Status:           models.SoftwareTaskStatusQueued,
		Phase:            models.SoftwareTaskStatusQueued,
		Progress:         0,
		Message:          queuedMessage,
		RollbackStatus:   models.SoftwareTaskRollbackNotRequired,
		RequestedBy:      requestedBy,
		EventSeq:         1,
		LogPath:          filepath.Join(m.logDir, "task_"+taskID+".log"),
		ParametersJSON:   string(safeParameters),
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	event := &models.SoftwareTaskEvent{
		TaskID:    task.ID,
		Seq:       1,
		Type:      "snapshot",
		Level:     "info",
		Status:    task.Status,
		Phase:     task.Phase,
		Progress:  task.Progress,
		Code:      "task_queued",
		Message:   task.Message,
		CreatedAt: now,
	}
	var configurationHistory *models.SoftwareConfigurationHistory
	if request.Operation == "configure" {
		beforeJSON, marshalErr := json.Marshal(request.PreviousConfiguration)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode previous component configuration: %w", marshalErr)
		}
		afterJSON, marshalErr := json.Marshal(request.Configuration)
		if marshalErr != nil {
			return nil, fmt.Errorf("encode target component configuration: %w", marshalErr)
		}
		configurationHistory = &models.SoftwareConfigurationHistory{
			ID:              uuid.NewString(),
			TaskID:          task.ID,
			Component:       component,
			SoftwareKey:     request.Key,
			SoftwareVersion: request.Version,
			BaseRevision:    request.Revision,
			BeforeJSON:      string(beforeJSON),
			AfterJSON:       string(afterJSON),
			Status:          models.SoftwareConfigurationStatusPending,
			RestoreFromID:   request.RestoreFromID,
			RequestedBy:     requestedBy,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}
	if err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		if configurationHistory != nil {
			return tx.Create(configurationHistory).Error
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("create software task: %w", err)
	}

	select {
	case m.queue <- queuedTask{taskID: taskID, request: request}:
		m.notify(taskID)
		return task, nil
	default:
		reporter := newReporter(m, taskID)
		_ = reporter.finish(
			models.SoftwareTaskStatusFailed,
			"QUEUE_FULL",
			"软件任务队列已满，请稍后重试",
		)
		return nil, errors.New("software task queue is full")
	}
}

func (m *Manager) worker() {
	for {
		if m.stopping.Load() {
			return
		}
		item := <-m.queue
		m.lifecycleMu.Lock()
		if m.stopping.Load() {
			m.lifecycleMu.Unlock()
			return
		}
		m.runWG.Add(1)
		m.lifecycleMu.Unlock()
		m.run(item)
		m.runWG.Done()
	}
}

func (m *Manager) run(item queuedTask) {
	var task models.SoftwareTask
	if err := m.db.First(&task, "id = ?", item.taskID).Error; err != nil {
		return
	}
	if models.IsSoftwareTaskTerminal(task.Status) || task.CancelRequested {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelMu.Lock()
	m.cancels[task.ID] = cancel
	m.cancelMu.Unlock()
	if m.stopping.Load() {
		cancel()
	}
	defer func() {
		cancel()
		m.cancelMu.Lock()
		delete(m.cancels, task.ID)
		m.cancelMu.Unlock()
	}()
	if m.isCancelRequested(task.ID) {
		return
	}
	if err := m.acquireComponentLock(&task); err != nil {
		reporter := newReporter(m, task.ID)
		_ = reporter.finish(models.SoftwareTaskStatusFailed, "COMPONENT_BUSY", err.Error())
		return
	}
	defer m.releaseComponentLock(task.Component, task.ID)
	if runtimeGroup := m.runtimeGroupForComponent(task.Component); runtimeGroup != "" {
		if err := m.acquireRuntimeGroupLock(&task, runtimeGroup); err != nil {
			reporter := newReporter(m, task.ID)
			_ = reporter.finish(models.SoftwareTaskStatusFailed, "RUNTIME_GROUP_BUSY", err.Error())
			return
		}
		defer m.releaseRuntimeGroupLock(runtimeGroup, task.ID)
		if (task.Operation == "start" || task.Operation == "restart") && !task.SwitchRequested {
			if owners := activeRuntimeGroupOwners(context.Background(), runtimeGroup, task.Component); len(owners) > 0 {
				reporter := newReporter(m, task.ID)
				_ = reporter.finish(models.SoftwareTaskStatusFailed, "RUNTIME_GROUP_BUSY", fmt.Sprintf("%s is already active", owners[0].ServiceName))
				return
			}
		}
	}

	heartbeatDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.Touch(task.ID)
			case <-heartbeatDone:
				return
			}
		}
	}()
	defer close(heartbeatDone)

	reporter := newReporter(m, task.ID)
	resolvingMessage := "正在解析并校验" + operationLabel(task.Operation) + "脚本包"
	if err := reporter.setPhase("resolving", nil, resolvingMessage, "package_resolving"); err != nil {
		return
	}
	err := m.executor(ctx, item.request, task.LogPath, reporter)
	if err == nil {
		finalMessage := operationSuccessMessage(task.Operation)
		_ = reporter.setPhase("finalizing", intPointer(0), "正在保存任务状态", "task_finalizing")
		_ = reporter.finish(models.SoftwareTaskStatusSucceeded, "", finalMessage)
		return
	}
	if errors.Is(err, context.Canceled) || m.isCancelRequested(task.ID) {
		if m.stopping.Load() && !m.isCancelRequested(task.ID) {
			shutdownMessage := "Panel 正在关闭，" + operationLabel(task.Operation) +
				"任务已安全中断；下次启动将重新核验组件状态"
			_ = reporter.finish(
				models.SoftwareTaskStatusInterrupted,
				"PANEL_SHUTDOWN",
				shutdownMessage,
			)
			return
		}
		cancelMessage := operationLabel(task.Operation) + "任务已取消"
		_ = reporter.finish(models.SoftwareTaskStatusCanceled, "ACTION_CANCELED", cancelMessage)
		return
	}
	code := classifyExecutionError(err)
	_ = reporter.finish(models.SoftwareTaskStatusFailed, code, safeErrorMessage(err))
}

// Stop rejects new submissions, cancels running component processes through
// their contexts, and waits for task finalization. Queued tasks remain durable
// and are reconciled as interrupted by the next Panel process.
func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.lifecycleMu.Lock()
	m.stopping.Store(true)
	m.lifecycleMu.Unlock()
	m.cancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.cancels))
	for _, cancel := range m.cancels {
		cancels = append(cancels, cancel)
	}
	m.cancelMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		m.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) acquireComponentLock(task *models.SoftwareTask) error {
	now := time.Now()
	lock := &models.ComponentOperationLock{
		Component:   task.Component,
		TaskID:      task.ID,
		AcquiredAt:  now,
		HeartbeatAt: now,
	}
	if err := m.db.Create(lock).Error; err != nil {
		return fmt.Errorf("component %s already has an operation in progress", task.Component)
	}
	return nil
}

func (m *Manager) releaseComponentLock(component, taskID string) {
	_ = m.db.
		Where("component = ? AND task_id = ?", component, taskID).
		Delete(&models.ComponentOperationLock{}).Error
}

func (m *Manager) acquireRuntimeGroupLock(task *models.SoftwareTask, runtimeGroup string) error {
	now := time.Now()
	lock := &models.RuntimeGroupOperationLock{
		RuntimeGroup: runtimeGroup,
		TaskID:       task.ID,
		AcquiredAt:   now,
		HeartbeatAt:  now,
	}
	if err := m.db.Create(lock).Error; err != nil {
		return fmt.Errorf("runtime group %s already has an operation in progress", runtimeGroup)
	}
	return nil
}

func (m *Manager) releaseRuntimeGroupLock(runtimeGroup, taskID string) {
	_ = m.db.Where("runtime_group = ? AND task_id = ?", runtimeGroup, taskID).
		Delete(&models.RuntimeGroupOperationLock{}).Error
}

func (m *Manager) isCancelRequested(taskID string) bool {
	var task models.SoftwareTask
	if err := m.db.Select("cancel_requested").First(&task, "id = ?", taskID).Error; err != nil {
		return false
	}
	return task.CancelRequested
}

func (m *Manager) reconcileInterruptedTasks() error {
	var tasks []models.SoftwareTask
	if err := m.db.Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Find(&tasks).Error; err != nil {
		return fmt.Errorf("list interrupted software tasks: %w", err)
	}
	inspections := make(map[string]RecoveryInspection, len(tasks))
	for i := range tasks {
		task := &tasks[i]
		inspection := RecoveryInspection{
			Status:  "unknown",
			Message: "未配置组件状态探测器",
		}
		if m.inspector != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			inspection = m.inspector(ctx, task)
			cancel()
		}
		inspection.Status = sanitizeRecoveryValue(inspection.Status, 32, "unknown")
		inspection.Message = sanitizeRecoveryValue(inspection.Message, 500, "组件状态未知")
		inspections[task.ID] = inspection
	}

	return m.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for i := range tasks {
			task := &tasks[i]
			inspection := inspections[task.ID]
			task.EventSeq++
			task.Status = models.SoftwareTaskStatusInterrupted
			task.Phase = models.SoftwareTaskStatusInterrupted
			task.RecoveryStatus = inspection.Status
			task.RecoveryMessage = inspection.Message
			task.Message = "Panel 重启导致任务中断；" + inspection.Message
			task.ErrorCode = "PANEL_RESTARTED"
			task.ErrorMessage = task.Message
			task.FinishedAt = &now
			task.HeartbeatAt = &now
			if err := tx.Save(task).Error; err != nil {
				return err
			}
			event := models.SoftwareTaskEvent{
				TaskID:    task.ID,
				Seq:       task.EventSeq,
				Type:      "terminal",
				Level:     "error",
				Status:    task.Status,
				Phase:     task.Phase,
				Progress:  task.Progress,
				Code:      task.ErrorCode,
				Message:   task.Message,
				CreatedAt: now,
			}
			if err := tx.Create(&event).Error; err != nil {
				return err
			}
			if task.Operation == "configure" {
				if err := tx.Model(&models.SoftwareConfigurationHistory{}).
					Where("task_id = ? AND status = ?", task.ID, models.SoftwareConfigurationStatusPending).
					Updates(map[string]any{
						"status":      models.SoftwareConfigurationStatusInterrupted,
						"finished_at": now,
						"updated_at":  now,
					}).Error; err != nil {
					return err
				}
			}
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&models.ComponentOperationLock{}).Error; err != nil {
			return fmt.Errorf("clear stale component task locks: %w", err)
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&models.RuntimeGroupOperationLock{}).Error; err != nil {
			return fmt.Errorf("clear stale runtime group task locks: %w", err)
		}
		return nil
	})
}

func sanitizeRecoveryValue(value string, limit int, fallback string) string {
	value = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, value))
	if value == "" {
		return fallback
	}
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

func (m *Manager) componentForKey(key string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(key))
	var catalogRow models.Software
	if err := m.db.
		Where("`key` = ? AND component <> ''", normalized).
		Order("catalog_managed DESC, catalog_visible DESC, id DESC").
		First(&catalogRow).Error; err == nil {
		return strings.ToLower(strings.TrimSpace(catalogRow.Component)), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("look up software component: %w", err)
	}
	switch normalized {
	case "webserver":
		return "nginx", nil
	case "db":
		return "mysql", nil
	case "redis", "php", "java", "openresty", "phpmyadmin", "firewalld":
		return strings.ToLower(strings.TrimSpace(key)), nil
	default:
		return "", fmt.Errorf("unsupported software: %s", key)
	}
}

func (m *Manager) softwareKeyForUninstall(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	var catalogRow models.Software
	if err := m.db.
		Where("(`key` = ? OR component = ?) AND installed = ?", normalized, normalized, true).
		First(&catalogRow).Error; err == nil {
		return catalogRow.Key, nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("look up installed software: %w", err)
	}
	switch normalized {
	case "nginx", "webserver":
		return "webserver", nil
	case "openresty", "tengine", "apache", "caddy":
		return normalized, nil
	case "mysql", "db":
		return "db", nil
	case "redis":
		return "redis", nil
	case "php":
		return "php", nil
	case "firewalld":
		return "firewalld", nil
	default:
		return "", fmt.Errorf("unsupported software for uninstall: %s", value)
	}
}

func (m *Manager) validateCatalogInstall(key, version string) error {
	if !m.db.Migrator().HasTable(&models.SoftwareCatalogState{}) {
		return nil
	}
	var state models.SoftwareCatalogState
	if err := m.db.First(&state, 1).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read software catalog state: %w", err)
	}
	if strings.TrimSpace(state.Revision) == "" {
		return nil
	}
	var catalogRow models.Software
	if err := m.db.
		Where("`key` = ? AND version = ?", key, version).
		First(&catalogRow).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("software %s %s is not published by Center", key, version)
	} else if err != nil {
		return fmt.Errorf("read Center software catalog entry: %w", err)
	}
	if !catalogRow.CatalogManaged || !catalogRow.CatalogVisible {
		return fmt.Errorf("software %s %s has been removed from the Center catalog", key, version)
	}
	if !catalogRow.Installable {
		return fmt.Errorf("software %s %s installation is disabled by Center", key, version)
	}
	return nil
}

func (m *Manager) validateExclusiveDatabaseInstall(component string) error {
	component = strings.ToLower(strings.TrimSpace(component))
	group := []string{"mysql", "mariadb", "percona"}
	matched := false
	for _, candidate := range group {
		if component == candidate {
			matched = true
			break
		}
	}
	if !matched {
		return nil
	}

	var installed models.Software
	err := m.db.
		Where("installed = ? AND component IN ? AND component <> ?", true, group, component).
		Order("id DESC").
		First(&installed).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check mutually exclusive database software: %w", err)
	}
	conflict := strings.TrimSpace(installed.Name)
	if conflict == "" {
		conflict = strings.TrimSpace(installed.Component)
	}
	return fmt.Errorf(
		"cannot install %s while %s is installed; uninstall the conflicting component first",
		component,
		conflict,
	)
}

func (m *Manager) runtimeGroupForComponent(component string) string {
	component = strings.ToLower(strings.TrimSpace(component))
	var row models.Software
	if m != nil && m.db != nil && m.db.Where("component = ?", component).
		Order("installed DESC, catalog_managed DESC, id DESC").First(&row).Error == nil {
		if group := strings.TrimSpace(row.RuntimeGroup); group != "" {
			return group
		}
	}
	if component == "nginx" || component == "openresty" || component == "tengine" || component == "caddy" || component == "apache" {
		return "web-server"
	}
	return ""
}

func activeRuntimeGroupOwners(ctx context.Context, runtimeGroup, excludeComponent string) []RuntimeGroupOwner {
	if strings.TrimSpace(runtimeGroup) == "" {
		return nil
	}
	units := []RuntimeGroupOwner{
		{Component: "nginx", ServiceName: "oneinstack-nginx"},
		{Component: "openresty", ServiceName: "oneinstack-openresty"},
		{Component: "tengine", ServiceName: "oneinstack-tengine"},
		{Component: "caddy", ServiceName: "oneinstack-caddy"},
		{Component: "apache", ServiceName: "oneinstack-httpd"},
		{Component: "legacy-web", ServiceName: "nginx"},
	}
	result := make([]RuntimeGroupOwner, 0, len(units))
	for _, owner := range units {
		if owner.Component == excludeComponent {
			continue
		}
		command := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", owner.ServiceName+".service")
		if err := command.Run(); err == nil {
			result = append(result, owner)
		}
	}
	return result
}

func (m *Manager) softwareKeyForService(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	var row models.Software
	if m != nil && m.db != nil {
		if err := m.db.Where(
			"installed = ? AND (`key` = ? OR component = ?)",
			true, normalized, normalized,
		).Order("install_time DESC, id DESC").First(&row).Error; err == nil {
			key := strings.ToLower(strings.TrimSpace(row.Key))
			if key != "" {
				return key, nil
			}
		}
		if err := m.db.Where(
			"catalog_managed = ? AND (`key` = ? OR component = ?)",
			true, normalized, normalized,
		).Order("catalog_visible DESC, id DESC").First(&row).Error; err == nil {
			key := strings.ToLower(strings.TrimSpace(row.Key))
			if key != "" {
				return key, nil
			}
		}
	}
	switch normalized {
	case "nginx", "webserver":
		return "webserver", nil
	case "mysql", "db":
		return "db", nil
	case "redis":
		return "redis", nil
	case "php", "php-fpm":
		return "php", nil
	default:
		return "", fmt.Errorf("unsupported component service: %s", value)
	}
}

func isServiceOperation(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "start", "stop", "restart", "reload":
		return true
	default:
		return false
	}
}

func isRuntimeOperation(value string) bool {
	return isServiceOperation(value) ||
		strings.EqualFold(strings.TrimSpace(value), "configure")
}

func cloneConfigurationValues(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func sameConfigurationKeys(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, exists := right[key]; !exists {
			return false
		}
	}
	return true
}

func validateConfigurationTaskPayload(revision string, values map[string]string) error {
	if len(revision) != 64 {
		return errors.New("configuration revision must be a SHA-256 digest")
	}
	for _, character := range revision {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return errors.New("configuration revision must be a SHA-256 digest")
		}
	}
	if len(values) < 1 || len(values) > 32 {
		return errors.New("configuration must contain between 1 and 32 managed fields")
	}
	for key, value := range values {
		if len(key) < 1 || len(key) > 64 ||
			key[0] < 'a' || key[0] > 'z' {
			return fmt.Errorf("configuration field name %q is invalid", key)
		}
		for _, character := range key[1:] {
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
				return fmt.Errorf("configuration field name %q is invalid", key)
			}
		}
		if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("configuration field %s contains invalid data", key)
		}
	}
	return nil
}

func operationLabel(operation string) string {
	switch operation {
	case "upgrade":
		return "升级"
	case "uninstall":
		return "卸载"
	case "start":
		return "启动"
	case "stop":
		return "停止"
	case "restart":
		return "重启"
	case "reload":
		return "重载"
	case "configure":
		return "配置"
	default:
		return "安装"
	}
}

func operationSuccessMessage(operation string) string {
	switch operation {
	case "uninstall":
		return "卸载完成，软件数据按组件策略保留"
	case "start":
		return "服务启动成功"
	case "stop":
		return "服务停止成功"
	case "restart":
		return "服务重启成功"
	case "reload":
		return "服务重载成功"
	case "configure":
		return "配置已安全发布并验证成功"
	case "upgrade":
		return "升级并验证成功"
	default:
		return "安装并验证成功"
	}
}

func classifyExecutionError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded), strings.Contains(message, "timeout"), strings.Contains(message, "exceeded timeout"):
		return "ACTION_TIMEOUT"
	case strings.Contains(message, "configuration revision conflict"),
		strings.Contains(message, "changed since preview"),
		strings.Contains(message, "configapply action failed: exit status 75"):
		return "CONFIG_CONFLICT"
	case strings.Contains(message, "candidate configuration is invalid"),
		strings.Contains(message, "configuration output is invalid"),
		strings.Contains(message, "configapply action failed: exit status 65"):
		return "CONFIG_VALIDATE_FAILED"
	case strings.Contains(message, "configapply action"),
		strings.Contains(message, "configuration package"),
		strings.Contains(message, "managed configuration"):
		return "CONFIG_APPLY_FAILED"
	case strings.Contains(message, "checksum"), strings.Contains(message, "signature"), strings.Contains(message, "verify package"):
		return "PACKAGE_VERIFY_FAILED"
	case strings.Contains(message, "no compatible package"),
		strings.Contains(message, "no compatible bundled"),
		strings.Contains(message, "no compatible cached"),
		strings.Contains(message, "package does not provide the required actions"),
		strings.Contains(message, "read bundled component"),
		strings.Contains(message, "read cached component"):
		return "PACKAGE_UNAVAILABLE"
	case strings.Contains(message, "script center connectivity"),
		strings.Contains(message, "script center is not ready"),
		strings.Contains(message, "script center returned http"),
		strings.Contains(message, "download package"):
		return "CENTER_UNAVAILABLE"
	case strings.Contains(message, "resolve"):
		return "PACKAGE_UNAVAILABLE"
	case strings.Contains(message, "precheck"):
		return "PRECHECK_FAILED"
	case strings.Contains(message, "configure"):
		return "CONFIGURE_FAILED"
	case strings.Contains(message, "verify"):
		return "VERIFY_FAILED"
	case strings.Contains(message, "rollback"):
		return "ROLLBACK_FAILED"
	case strings.Contains(message, "uninstall"):
		return "UNINSTALL_FAILED"
	case strings.Contains(message, "start action"):
		return "SERVICE_START_FAILED"
	case strings.Contains(message, "stop action"):
		return "SERVICE_STOP_FAILED"
	case strings.Contains(message, "restart action"):
		return "SERVICE_RESTART_FAILED"
	case strings.Contains(message, "reload action"), strings.Contains(message, "has no reload action"):
		return "SERVICE_RELOAD_FAILED"
	default:
		return "INSTALL_FAILED"
	}
}

func safeErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	message := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' {
			return -1
		}
		return r
	}, err.Error())
	if len(message) > 1000 {
		message = message[:1000]
	}
	return message
}

func intPointer(value int) *int {
	result := value
	return &result
}
