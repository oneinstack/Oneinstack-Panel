package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/internal/services/scriptregistry"
	softwareService "oneinstack/internal/services/software"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	TaskServiceActionPreflight = "service.action.preflight.v1"
	TaskServiceActionExecute   = "service.action.execute.v1"
	CapabilityServiceAction    = "service.action.v1"

	serviceActionBatchAction    = "service_action"
	serviceActionStopConfirm    = "STOP SERVICES"
	serviceActionRestartConfirm = "RESTART SERVICES"
)

var (
	ErrServiceActionInput   = errors.New("service action request is invalid")
	ErrServiceActionPreview = errors.New("service action preview is invalid or expired")
	ErrServiceActionStale   = errors.New("service action preview is stale")
	ErrServiceActionConfirm = errors.New("service action confirmation is invalid")
)

// ServiceActionPreviewInput exposes only a component identifier and one fixed
// lifecycle action. It deliberately has no command, argv, environment, path,
// or script fields.
type ServiceActionPreviewInput struct {
	NodeIDs   []uint `json:"nodeIds"`
	Component string `json:"component"`
	Action    string `json:"action"`
}

type ServiceActionPreviewResult struct {
	ID          string             `json:"id"`
	Component   string             `json:"component"`
	DisplayName string             `json:"displayName"`
	Action      string             `json:"action"`
	Executable  []BatchPreviewItem `json:"executable"`
	Blocked     []BatchPreviewItem `json:"blocked"`
	Skipped     []BatchPreviewItem `json:"skipped"`
	Impact      string             `json:"impact"`
	ConfirmText string             `json:"confirmText,omitempty"`
	Fingerprint string             `json:"fingerprint"`
	ExpiresAt   time.Time          `json:"expiresAt"`
}

type ExecuteServiceActionInput struct {
	PreviewID   string `json:"previewId"`
	Fingerprint string `json:"fingerprint"`
	Confirm     string `json:"confirm,omitempty"`
}

type ServiceActionList struct {
	NodeID     uint                                    `json:"nodeId"`
	Items      []models.ClusterServiceActionCapability `json:"items"`
	ReportedAt *time.Time                              `json:"reportedAt,omitempty"`
}

type serviceActionPreviewPayload struct {
	NodeIDs   []uint `json:"nodeIds"`
	Component string `json:"component"`
	Action    string `json:"action"`
}

type serviceActionTaskPayload struct {
	WorkflowID string                     `json:"workflowId"`
	Component  string                     `json:"component"`
	Action     string                     `json:"action"`
	Version    string                     `json:"version,omitempty"`
	PackagePin *scriptregistry.PackagePin `json:"packagePin,omitempty"`
}

type serviceActionPreflightResult struct {
	Component   string                    `json:"component"`
	DisplayName string                    `json:"displayName"`
	Action      string                    `json:"action"`
	Version     string                    `json:"version"`
	ServiceName string                    `json:"serviceName"`
	ActiveState string                    `json:"activeState"`
	PackagePin  scriptregistry.PackagePin `json:"packagePin"`
}

type serviceActionExecutionResult struct {
	Component     string `json:"component"`
	DisplayName   string `json:"displayName"`
	Action        string `json:"action"`
	Version       string `json:"version"`
	ServiceName   string `json:"serviceName"`
	PackageID     string `json:"packageId"`
	PackageSHA256 string `json:"packageSHA256"`
}

func isServiceActionTask(taskType string) bool {
	return taskType == TaskServiceActionPreflight || taskType == TaskServiceActionExecute
}

func normalizeServiceActionCapabilities(values []models.ClusterServiceActionCapability) []models.ClusterServiceActionCapability {
	seen := make(map[string]struct{}, len(values))
	result := make([]models.ClusterServiceActionCapability, 0, len(values))
	for _, value := range values {
		component := normalizeServiceActionComponent(value.Component)
		if component == "" {
			continue
		}
		actions := make([]string, 0, len(value.AvailableActions))
		for _, action := range value.AvailableActions {
			action = strings.ToLower(strings.TrimSpace(action))
			if softwareService.IsServiceAction(action) && !containsString(actions, action) {
				actions = append(actions, action)
			}
		}
		if len(actions) == 0 {
			continue
		}
		sort.Strings(actions)
		if _, exists := seen[component]; exists {
			continue
		}
		seen[component] = struct{}{}
		result = append(result, models.ClusterServiceActionCapability{
			Component: component, DisplayName: boundedServiceActionText(value.DisplayName, 160),
			ServiceName:     boundedServiceActionText(value.ServiceName, 160),
			SoftwareVersion: boundedServiceActionText(value.SoftwareVersion, 120),
			ActiveState:     boundedServiceActionText(value.ActiveState, 32), AvailableActions: actions,
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Component < result[j].Component })
	return result
}

func (m *Manager) ListNodeServiceActions(nodeID uint) (ServiceActionList, error) {
	node, err := m.GetNode(nodeID)
	if err != nil {
		return ServiceActionList{}, err
	}
	return ServiceActionList{
		NodeID: node.ID, Items: normalizeServiceActionCapabilities(node.ServiceActions), ReportedAt: node.ServiceActionsReportedAt,
	}, nil
}

func (m *Manager) PreviewServiceAction(input ServiceActionPreviewInput) (ServiceActionPreviewResult, error) {
	result, payload, err := m.evaluateServiceAction(input)
	if err != nil {
		return ServiceActionPreviewResult{}, err
	}
	result.ID = uuid.NewString()
	result.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	encoded, _ := json.Marshal(payload)
	row := models.ClusterBatchPreview{
		ID: result.ID, Action: serviceActionBatchAction, NodeIDs: payload.NodeIDs,
		Fingerprint: result.Fingerprint, Payload: string(encoded), ExpiresAt: result.ExpiresAt,
	}
	if err := m.db.Create(&row).Error; err != nil {
		return ServiceActionPreviewResult{}, err
	}
	return result, nil
}

func (m *Manager) evaluateServiceAction(input ServiceActionPreviewInput) (ServiceActionPreviewResult, serviceActionPreviewPayload, error) {
	component := normalizeServiceActionComponent(input.Component)
	action := strings.ToLower(strings.TrimSpace(input.Action))
	ids := normalizedNodeIDs(input.NodeIDs)
	if component == "" || !softwareService.IsServiceAction(action) || len(ids) == 0 || len(ids) > 100 {
		return ServiceActionPreviewResult{}, serviceActionPreviewPayload{}, ErrServiceActionInput
	}
	payload := serviceActionPreviewPayload{NodeIDs: ids, Component: component, Action: action}
	result := ServiceActionPreviewResult{Component: component, Action: action}
	fingerprintParts := []string{component, action}
	for _, id := range ids {
		node, err := m.GetNode(id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result.Skipped = append(result.Skipped, BatchPreviewItem{NodeID: id, Reason: "节点不存在或已删除"})
			fingerprintParts = append(fingerprintParts, fmt.Sprintf("%d:missing", id))
			continue
		}
		if err != nil {
			return ServiceActionPreviewResult{}, serviceActionPreviewPayload{}, err
		}
		item := BatchPreviewItem{NodeID: node.ID, Name: node.Name}
		capability, reason := serviceActionNodeCapability(node, component, action)
		fingerprintParts = append(fingerprintParts, serviceActionFingerprintPart(node, capability, component, action))
		if reason != "" {
			item.Reason = reason
			result.Blocked = append(result.Blocked, item)
			continue
		}
		if result.DisplayName == "" {
			result.DisplayName = capability.DisplayName
		}
		result.Executable = append(result.Executable, item)
	}
	if result.DisplayName == "" {
		result.DisplayName = component
	}
	result.Impact, result.ConfirmText = serviceActionImpact(result.DisplayName, action, len(result.Executable))
	sum := sha256.Sum256([]byte(strings.Join(fingerprintParts, "|")))
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, payload, nil
}

func (m *Manager) ExecuteServiceAction(input ExecuteServiceActionInput, requestedBy int64) (models.ClusterBatchOperation, error) {
	var stored models.ClusterBatchPreview
	if err := m.db.First(&stored, "id = ? AND action = ?", strings.TrimSpace(input.PreviewID), serviceActionBatchAction).Error; err != nil {
		return models.ClusterBatchOperation{}, ErrServiceActionPreview
	}
	if time.Now().After(stored.ExpiresAt) {
		return models.ClusterBatchOperation{}, ErrServiceActionPreview
	}
	var payload serviceActionPreviewPayload
	if json.Unmarshal([]byte(stored.Payload), &payload) != nil {
		return models.ClusterBatchOperation{}, ErrServiceActionPreview
	}
	current, normalized, err := m.evaluateServiceAction(ServiceActionPreviewInput(payload))
	if err != nil {
		return models.ClusterBatchOperation{}, err
	}
	if current.Fingerprint != stored.Fingerprint || current.Fingerprint != strings.TrimSpace(input.Fingerprint) {
		return models.ClusterBatchOperation{}, ErrServiceActionStale
	}
	if current.ConfirmText != "" && strings.TrimSpace(input.Confirm) != current.ConfirmText {
		return models.ClusterBatchOperation{}, ErrServiceActionConfirm
	}
	if len(current.Executable) == 0 {
		return models.ClusterBatchOperation{}, errors.New("没有可执行的节点")
	}
	policy, err := m.GetPolicy()
	if err != nil {
		return models.ClusterBatchOperation{}, err
	}
	nodeIDs := make([]uint, 0, len(current.Executable))
	for _, item := range current.Executable {
		nodeIDs = append(nodeIDs, item.NodeID)
	}
	encoded, _ := json.Marshal(normalized)
	now := time.Now()
	batch := models.ClusterBatchOperation{
		ID: uuid.NewString(), Action: serviceActionBatchAction, Payload: string(encoded), Status: models.ClusterTaskStatusQueued,
		NodeIDs: nodeIDs, Total: len(nodeIDs), Queued: len(nodeIDs), MaxConcurrency: maxServiceActionConcurrency(policy.LifecycleConcurrency),
		RequestedBy: requestedBy, StartedAt: &now,
	}
	if err := m.db.Create(&batch).Error; err != nil {
		return models.ClusterBatchOperation{}, err
	}
	for _, nodeID := range nodeIDs {
		taskPayload, _ := json.Marshal(serviceActionTaskPayload{WorkflowID: batch.ID, Component: normalized.Component, Action: normalized.Action})
		preflight, enqueueErr := m.EnqueueTask(EnqueueTaskInput{
			NodeID: nodeID, WorkflowID: batch.ID, Type: TaskServiceActionPreflight, Payload: taskPayload,
			IdempotencyKey: serviceActionTaskKey(batch.ID, nodeID, TaskServiceActionPreflight), MaxAttempts: 1,
			RequestedBy: requestedBy, internal: true,
		})
		if enqueueErr != nil {
			if _, err := m.createTerminalServiceActionTask(batch, nodeID, models.ClusterTaskStatusFailed, "SERVICE_ACTION_PREFLIGHT_FAILED", normalized); err != nil {
				return models.ClusterBatchOperation{}, err
			}
			continue
		}
		recordServiceActionAudit(preflight, "preflight_queued")
	}
	if err := m.updateBatchStatus(batch.ID); err != nil {
		return models.ClusterBatchOperation{}, err
	}
	if err := m.db.First(&batch, "id = ?", batch.ID).Error; err != nil {
		return models.ClusterBatchOperation{}, err
	}
	return batch, nil
}

// advanceServiceActionPreflight turns a successful node-local package resolve
// into exactly one fixed execution task. Preflight tasks are workflow-linked
// rather than batch children so batch totals continue to represent one final
// action per node.
func (m *Manager) advanceServiceActionPreflight(task models.ClusterTask) error {
	if task.Type != TaskServiceActionPreflight || !isTerminalTaskStatus(task.Status) || strings.TrimSpace(task.WorkflowID) == "" {
		return nil
	}
	var payload serviceActionTaskPayload
	if json.Unmarshal([]byte(task.Payload), &payload) != nil || strings.TrimSpace(payload.WorkflowID) == "" {
		return nil
	}
	var existing models.ClusterTask
	err := m.db.Where("workflow_id = ? AND node_id = ? AND type = ?", payload.WorkflowID, task.NodeID, TaskServiceActionExecute).First(&existing).Error
	if err == nil {
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var batch models.ClusterBatchOperation
	if err := m.db.First(&batch, "id = ?", payload.WorkflowID).Error; err != nil {
		return err
	}
	executionStatus := models.ClusterTaskStatusFailed
	executionError := "SERVICE_ACTION_PREFLIGHT_FAILED"
	executionPayload := serviceActionTaskPayload{WorkflowID: batch.ID, Component: payload.Component, Action: payload.Action}
	if task.Status == models.ClusterTaskStatusCanceled || batch.Status == models.ClusterTaskStatusCanceled {
		executionStatus, executionError = models.ClusterTaskStatusCanceled, "SERVICE_ACTION_CANCELED"
	} else if task.Status == models.ClusterTaskStatusSucceeded {
		node, nodeErr := m.GetNode(task.NodeID)
		if nodeErr != nil {
			return m.createAndUpdateTerminalServiceActionTask(batch, task.NodeID, executionStatus, "SERVICE_ACTION_NODE_STATE_CHANGED", executionPayload)
		}
		if _, reason := serviceActionNodeCapability(node, payload.Component, payload.Action); reason != "" {
			return m.createAndUpdateTerminalServiceActionTask(batch, task.NodeID, executionStatus, "SERVICE_ACTION_NODE_STATE_CHANGED", executionPayload)
		}
		var result serviceActionPreflightResult
		if json.Unmarshal([]byte(task.Result), &result) == nil && validServiceActionPreflight(payload, result) {
			executionPayload.Version = result.Version
			pin := result.PackagePin
			executionPayload.PackagePin = &pin
			encoded, _ := json.Marshal(executionPayload)
			execution, enqueueErr := m.EnqueueTask(EnqueueTaskInput{
				NodeID: task.NodeID, BatchID: batch.ID, WorkflowID: batch.ID, Type: TaskServiceActionExecute, Payload: encoded,
				IdempotencyKey: serviceActionTaskKey(batch.ID, task.NodeID, TaskServiceActionExecute), MaxAttempts: 1,
				RequestedBy: batch.RequestedBy, internal: true,
			})
			if enqueueErr == nil {
				recordServiceActionAudit(execution, "execution_queued")
				return m.updateBatchStatus(batch.ID)
			}
			executionError = "SERVICE_ACTION_EXECUTION_QUEUED_FAILED"
		}
	}
	return m.createAndUpdateTerminalServiceActionTask(batch, task.NodeID, executionStatus, executionError, executionPayload)
}

func (m *Manager) createAndUpdateTerminalServiceActionTask(batch models.ClusterBatchOperation, nodeID uint, status, code string, payload serviceActionTaskPayload) error {
	if _, err := m.createTerminalServiceActionTask(batch, nodeID, status, code, serviceActionPreviewPayload{Component: payload.Component, Action: payload.Action}); err != nil {
		return err
	}
	return m.updateBatchStatus(batch.ID)
}

func (m *Manager) createTerminalServiceActionTask(batch models.ClusterBatchOperation, nodeID uint, status, code string, payload serviceActionPreviewPayload) (models.ClusterTask, error) {
	now := time.Now()
	encoded, _ := json.Marshal(serviceActionTaskPayload{WorkflowID: batch.ID, Component: payload.Component, Action: payload.Action})
	task := models.ClusterTask{
		NodeID: nodeID, BatchID: batch.ID, WorkflowID: batch.ID, Type: TaskServiceActionExecute,
		IdempotencyKey: serviceActionTaskKey(batch.ID, nodeID, TaskServiceActionExecute), Payload: string(encoded),
		Status: status, Stage: status, Progress: 100, MaxAttempts: 1, RequestedBy: batch.RequestedBy,
		Cancelable: false, QueuedAt: now, StartedAt: &now, FinishedAt: &now, Error: code,
	}
	if err := m.db.Create(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	level := "error"
	message := "服务操作预检未通过，未执行目标动作"
	if status == models.ClusterTaskStatusCanceled {
		level, message = "warning", "服务操作已取消，未执行目标动作"
	}
	_ = m.appendTaskEvent(task.ID, status, status, level, strings.ToLower(code), 100, message)
	recordServiceActionAudit(task, "execution_blocked")
	return task, nil
}

func serviceActionNodeCapability(node models.ClusterNode, component, action string) (models.ClusterServiceActionCapability, string) {
	if !nodeHeartbeatFresh(node, time.Now()) {
		return models.ClusterServiceActionCapability{}, "节点离线或心跳已过期"
	}
	if node.LifecycleStatus == models.ClusterNodeLifecycleDraining || node.LifecycleStatus == models.ClusterNodeLifecycleDrained ||
		node.LifecycleStatus == models.ClusterNodeLifecycleDisabled || node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete {
		return models.ClusterServiceActionCapability{}, "当前节点生命周期状态不允许下发服务操作"
	}
	if !hasCapability(node.Capabilities, CapabilityServiceAction) {
		return models.ClusterServiceActionCapability{}, "Agent 版本不支持受控服务操作，请先升级 Agent"
	}
	if node.ServiceActionsReportedAt == nil {
		return models.ClusterServiceActionCapability{}, "节点尚未上报可管理服务，请等待下一次心跳"
	}
	for _, value := range normalizeServiceActionCapabilities(node.ServiceActions) {
		if value.Component != component {
			continue
		}
		if !containsString(value.AvailableActions, action) {
			return models.ClusterServiceActionCapability{}, "目标节点组件包不支持该服务动作"
		}
		return value, ""
	}
	return models.ClusterServiceActionCapability{}, "目标节点未安装该组件或尚未通过服务探测"
}

func serviceActionFingerprintPart(node models.ClusterNode, capability models.ClusterServiceActionCapability, component, action string) string {
	return fmt.Sprintf("%d:%s:%s:%t:%s:%s:%s:%s", node.ID, node.Status, node.LifecycleStatus, node.Enabled,
		component, action, capability.SoftwareVersion, strings.Join(capability.AvailableActions, ","))
}

func serviceActionImpact(displayName, action string, count int) (string, string) {
	switch action {
	case "stop":
		return fmt.Sprintf("将停止 %d 个节点上的 %s 服务，业务可能立即中断", count, displayName), serviceActionStopConfirm
	case "restart":
		return fmt.Sprintf("将重启 %d 个节点上的 %s 服务，业务将短暂不可用", count, displayName), serviceActionRestartConfirm
	case "reload":
		return fmt.Sprintf("将向 %d 个节点下发 %s 服务重载", count, displayName), ""
	default:
		return fmt.Sprintf("将启动 %d 个节点上的 %s 服务", count, displayName), ""
	}
}

func validServiceActionPreflight(payload serviceActionTaskPayload, result serviceActionPreflightResult) bool {
	return result.Component == payload.Component && result.Action == payload.Action && strings.TrimSpace(result.Version) != "" &&
		result.PackagePin.Component == payload.Component && result.PackagePin.SoftwareVersion == result.Version &&
		strings.TrimSpace(result.PackagePin.ResolvedVersion) != "" && strings.TrimSpace(result.PackagePin.PackageSHA256) != ""
}

func serviceActionTaskKey(workflowID string, nodeID uint, taskType string) string {
	return fmt.Sprintf("service-action:%s:%d:%s", strings.TrimSpace(workflowID), nodeID, taskType)
}

func maxServiceActionConcurrency(value int) int {
	if value < 1 {
		return 1
	}
	if value > 20 {
		return 20
	}
	return value
}

func normalizeServiceActionComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 120 {
		return ""
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '-' {
			return ""
		}
	}
	return value
}

func boundedServiceActionText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}

func isTerminalTaskStatus(status string) bool {
	return status == models.ClusterTaskStatusSucceeded || status == models.ClusterTaskStatusFailed || status == models.ClusterTaskStatusCanceled
}

func serviceActionErrorCode(err error, phase string) string {
	value := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(value, "HOST_PLATFORM_UNSUPPORTED"):
		return "SERVICE_ACTION_HOST_UNSUPPORTED"
	case strings.Contains(value, "PACKAGE_") || strings.Contains(value, "CENTER_"):
		return "SERVICE_ACTION_PACKAGE_UNAVAILABLE"
	case strings.Contains(value, "UNSUPPORTED") || strings.Contains(value, "NOT INSTALLED"):
		return "SERVICE_ACTION_UNSUPPORTED"
	case phase == "preflight":
		return "SERVICE_ACTION_PREFLIGHT_FAILED"
	default:
		return "SERVICE_ACTION_FAILED"
	}
}

func safeServiceActionErrorCode(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	switch value {
	case "SERVICE_ACTION_CANCELED", "SERVICE_ACTION_PREFLIGHT_FAILED", "SERVICE_ACTION_HOST_UNSUPPORTED",
		"SERVICE_ACTION_PACKAGE_UNAVAILABLE", "SERVICE_ACTION_UNSUPPORTED", "SERVICE_ACTION_EXECUTION_QUEUED_FAILED",
		"SERVICE_ACTION_NODE_STATE_CHANGED", "SERVICE_ACTION_FAILED":
		return value
	default:
		return ""
	}
}

func publicServiceActionResult(raw string) json.RawMessage {
	var execution serviceActionExecutionResult
	if json.Unmarshal([]byte(raw), &execution) == nil && execution.Component != "" && execution.PackageID != "" {
		result, _ := json.Marshal(execution)
		return result
	}
	var preflight serviceActionPreflightResult
	if json.Unmarshal([]byte(raw), &preflight) == nil && preflight.Component != "" {
		result, _ := json.Marshal(map[string]string{
			"component": preflight.Component, "displayName": preflight.DisplayName, "action": preflight.Action,
			"version": preflight.Version, "serviceName": preflight.ServiceName, "activeState": preflight.ActiveState,
			"packageId": preflight.PackagePin.ResolvedVersion, "packageSHA256": preflight.PackagePin.PackageSHA256,
		})
		return result
	}
	return nil
}

// recordServiceActionAudit keeps an immutable, payload-free operator record
// for each state transition that matters to an operation. Package URLs,
// scripts, paths, and raw command output are intentionally excluded.
func recordServiceActionAudit(task models.ClusterTask, outcome string) {
	if !isServiceActionTask(task.Type) {
		return
	}
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	var payload serviceActionTaskPayload
	_ = json.Unmarshal([]byte(task.Payload), &payload)
	phase := "execute"
	if task.Type == TaskServiceActionPreflight {
		phase = "preflight"
	}
	status := 202
	if isTerminalTaskStatus(task.Status) {
		status = 200
		if task.Status == models.ClusterTaskStatusFailed {
			status = 500
		}
	}
	message := fmt.Sprintf(
		"node=%d task=%d workflow=%s component=%s action=%s phase=%s result=%s",
		task.NodeID, task.ID, boundedServiceActionText(task.WorkflowID, 64),
		boundedServiceActionText(payload.Component, 120), boundedServiceActionText(payload.Action, 16),
		phase, boundedServiceActionText(task.Status, 16),
	)
	if code := safeServiceActionErrorCode(task.Error); code != "" {
		message += " error_code=" + code
	}
	_, _ = manager.Append(auditservice.EventInput{
		EventType: "cluster", Action: "cluster.service_action." + phase,
		Status: status, Outcome: boundedServiceActionText(outcome, 64), Sensitive: true,
		UserID: task.RequestedBy, Message: message, CreatedAt: time.Now().UTC(),
	})
}
