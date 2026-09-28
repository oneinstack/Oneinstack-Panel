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

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	BatchActionDiagnose    = "diagnose"
	BatchActionRestart     = "restart"
	BatchActionUpdateCheck = "update_check"
	BatchActionUpdateApply = "update_apply"
	BatchActionDelete      = "delete"
)

var (
	ErrBatchAction       = errors.New("unsupported batch action")
	ErrBatchPreview      = errors.New("batch preview is invalid or expired")
	ErrBatchPreviewStale = errors.New("batch preview is stale")
	ErrBatchConfirm      = errors.New("batch confirmation text is invalid")
)

type BatchPreviewInput struct {
	Action  string `json:"action"`
	NodeIDs []uint `json:"nodeIds"`
}

type BatchPreviewItem struct {
	NodeID uint   `json:"nodeId"`
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

type BatchPreviewResult struct {
	ID           string             `json:"id"`
	Action       string             `json:"action"`
	Executable   []BatchPreviewItem `json:"executable"`
	Skipped      []BatchPreviewItem `json:"skipped"`
	Blocked      []BatchPreviewItem `json:"blocked"`
	Impact       string             `json:"impact"`
	ConfirmText  string             `json:"confirmText,omitempty"`
	Fingerprint  string             `json:"fingerprint"`
	ExpiresAt    time.Time          `json:"expiresAt"`
	ExpectedByID map[uint]string    `json:"expectedVersionByNode,omitempty"`
}

type ExecuteBatchInput struct {
	PreviewID   string `json:"previewId"`
	Fingerprint string `json:"fingerprint"`
	Confirm     string `json:"confirm,omitempty"`
}

type BatchList struct {
	Items    []models.ClusterBatchOperation `json:"items"`
	Total    int64                          `json:"total"`
	Page     int                            `json:"page"`
	PageSize int                            `json:"pageSize"`
}

type BatchDetail struct {
	models.ClusterBatchOperation
	Tasks []ClusterTaskSummary `json:"tasks"`
}

func (m *Manager) PreviewBatch(input BatchPreviewInput) (BatchPreviewResult, error) {
	result, err := m.evaluateBatch(input)
	if err != nil {
		return BatchPreviewResult{}, err
	}
	result.ID = uuid.NewString()
	result.ExpiresAt = time.Now().UTC().Add(10 * time.Minute)
	payload, _ := json.Marshal(input)
	row := models.ClusterBatchPreview{
		ID: result.ID, Action: result.Action, NodeIDs: normalizedNodeIDs(input.NodeIDs),
		Fingerprint: result.Fingerprint, Payload: string(payload), ExpiresAt: result.ExpiresAt,
	}
	if err := m.db.Create(&row).Error; err != nil {
		return BatchPreviewResult{}, err
	}
	return result, nil
}

func (m *Manager) evaluateBatch(input BatchPreviewInput) (BatchPreviewResult, error) {
	action := strings.TrimSpace(input.Action)
	if !validBatchAction(action) {
		return BatchPreviewResult{}, ErrBatchAction
	}
	ids := normalizedNodeIDs(input.NodeIDs)
	if len(ids) == 0 || len(ids) > 100 {
		return BatchPreviewResult{}, errors.New("节点数量必须在 1 到 100 之间")
	}
	result := BatchPreviewResult{Action: action, ExpectedByID: map[uint]string{}}
	fingerprintParts := []string{action}
	for _, id := range ids {
		node, err := m.GetNode(id)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			result.Skipped = append(result.Skipped, BatchPreviewItem{NodeID: id, Reason: "节点不存在或已删除"})
			fingerprintParts = append(fingerprintParts, fmt.Sprintf("%d:missing", id))
			continue
		}
		if err != nil {
			return BatchPreviewResult{}, err
		}
		item := BatchPreviewItem{NodeID: id, Name: node.Name}
		reason, blocked, expected := m.batchNodeBlockReason(node, action)
		fingerprintParts = append(fingerprintParts, fmt.Sprintf("%d:%s:%s:%t:%d:%s", node.ID, node.Status, node.LifecycleStatus, node.Enabled, node.UpdatedAt.UnixNano(), expected))
		if expected != "" {
			result.ExpectedByID[node.ID] = expected
		}
		if reason == "" {
			result.Executable = append(result.Executable, item)
		} else if blocked {
			item.Reason = reason
			result.Blocked = append(result.Blocked, item)
		} else {
			item.Reason = reason
			result.Skipped = append(result.Skipped, item)
		}
	}
	result.Impact, result.ConfirmText = batchImpact(action, len(result.Executable))
	sum := sha256.Sum256([]byte(strings.Join(fingerprintParts, "|")))
	result.Fingerprint = hex.EncodeToString(sum[:])
	return result, nil
}

func (m *Manager) batchNodeBlockReason(node models.ClusterNode, action string) (string, bool, string) {
	switch action {
	case BatchActionDiagnose:
		if node.Status == models.ClusterNodeStatusPending || node.LastRegisteredAt == nil {
			return "节点尚未注册，无法执行主机诊断", true, ""
		}
		if node.LifecycleStatus == models.ClusterNodeLifecycleDraining {
			return "节点正在排空，暂不接受新的诊断任务", true, ""
		}
		if !nodeHeartbeatFresh(node, time.Now()) {
			return "", false, ""
		}
		if !hasCapability(node.Capabilities, CapabilityNodeDiagnose) {
			return "Agent 版本不支持一键诊断，请先升级", true, ""
		}
	case BatchActionRestart:
		if !nodeHeartbeatFresh(node, time.Now()) {
			return "节点离线或心跳已过期", true, ""
		}
		if node.LifecycleStatus == models.ClusterNodeLifecycleDisabled || node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete || node.LifecycleStatus == models.ClusterNodeLifecycleDraining {
			return "当前生命周期状态不允许重启", true, ""
		}
	case BatchActionUpdateCheck:
		if node.LifecycleStatus == models.ClusterNodeLifecycleDraining {
			return "节点正在排空，暂不接受新的更新任务", true, ""
		}
		if _, err := m.panelUpdateNode(node.ID, CapabilityPanelUpdateCheck); err != nil {
			return "节点离线或 Agent 不支持更新检查", true, ""
		}
	case BatchActionUpdateApply:
		if node.LifecycleStatus == models.ClusterNodeLifecycleDraining {
			return "节点正在排空，暂不接受新的更新任务", true, ""
		}
		if _, err := m.panelUpdateNode(node.ID, CapabilityPanelUpdateApply); err != nil {
			return "节点离线或 Agent 不支持执行更新", true, ""
		}
		state, err := m.GetPanelUpdateState(node.ID)
		if err != nil || state.LastCheck == nil || !state.LastCheck.UpdateAvailable || !state.LastCheck.Compatible {
			return "没有有效且兼容的更新检查结果", true, ""
		}
		return "", false, state.LastCheck.LatestVersion
	case BatchActionDelete:
		if node.LifecycleStatus != models.ClusterNodeLifecyclePendingDelete {
			return "节点尚未标记为待删除", true, ""
		}
	default:
		if isLifecycleAction(action) {
			if err := m.validateLifecycle(node.ID, action); err != nil {
				return "当前生命周期状态不允许执行此操作", true, ""
			}
		}
	}
	return "", false, ""
}

func (m *Manager) ExecuteBatch(input ExecuteBatchInput, requestedBy int64) (models.ClusterBatchOperation, error) {
	var stored models.ClusterBatchPreview
	if err := m.db.First(&stored, "id = ?", strings.TrimSpace(input.PreviewID)).Error; err != nil {
		return models.ClusterBatchOperation{}, ErrBatchPreview
	}
	if time.Now().After(stored.ExpiresAt) {
		return models.ClusterBatchOperation{}, ErrBatchPreview
	}
	var previewInput BatchPreviewInput
	if json.Unmarshal([]byte(stored.Payload), &previewInput) != nil {
		return models.ClusterBatchOperation{}, ErrBatchPreview
	}
	current, err := m.evaluateBatch(previewInput)
	if err != nil {
		return models.ClusterBatchOperation{}, err
	}
	if current.Fingerprint != stored.Fingerprint || current.Fingerprint != strings.TrimSpace(input.Fingerprint) {
		return models.ClusterBatchOperation{}, ErrBatchPreviewStale
	}
	if current.ConfirmText != "" && input.Confirm != current.ConfirmText {
		return models.ClusterBatchOperation{}, ErrBatchConfirm
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
	now := time.Now()
	batch := models.ClusterBatchOperation{
		ID: uuid.NewString(), Action: current.Action, Status: models.ClusterTaskStatusQueued,
		NodeIDs: nodeIDs, Total: len(nodeIDs), Queued: len(nodeIDs),
		MaxConcurrency: batchConcurrency(current.Action, policy), RequestedBy: requestedBy,
		StartedAt: &now,
	}
	if err := m.db.Create(&batch).Error; err != nil {
		return models.ClusterBatchOperation{}, err
	}
	for _, nodeID := range nodeIDs {
		if err := m.executeBatchNode(batch, nodeID, current.ExpectedByID[nodeID]); err != nil {
			var existing int64
			if countErr := m.db.Model(&models.ClusterTask{}).Where("batch_id = ? AND node_id = ?", batch.ID, nodeID).Count(&existing).Error; countErr != nil {
				return models.ClusterBatchOperation{}, fmt.Errorf("检查批次子任务失败: %w", countErr)
			}
			if existing == 0 {
				if _, createErr := m.createTerminalBatchTask(batch, nodeID, models.ClusterTaskStatusFailed, err.Error()); createErr != nil {
					return models.ClusterBatchOperation{}, fmt.Errorf("创建批次子任务失败: %v；记录失败状态失败: %w", err, createErr)
				}
			}
		}
	}
	if err := m.updateBatchStatus(batch.ID); err != nil {
		return models.ClusterBatchOperation{}, fmt.Errorf("更新批次状态失败: %w", err)
	}
	if err := m.db.First(&batch, "id = ?", batch.ID).Error; err != nil {
		return models.ClusterBatchOperation{}, err
	}
	return batch, nil
}

func (m *Manager) executeBatchNode(batch models.ClusterBatchOperation, nodeID uint, expectedVersion string) error {
	switch batch.Action {
	case BatchActionDiagnose:
		_, err := m.EnqueueDiagnosis(nodeID, batch.ID, batch.RequestedBy)
		return err
	case BatchActionRestart:
		cancelable := true
		_, err := m.EnqueueTask(EnqueueTaskInput{NodeID: nodeID, BatchID: batch.ID, Type: "panel.restart", Payload: json.RawMessage(`{}`), IdempotencyKey: batchTaskIdempotencyKey(batch.ID, nodeID, "panel.restart"), MaxAttempts: 1, RequestedBy: batch.RequestedBy, Cancelable: &cancelable, internal: true})
		return err
	case BatchActionUpdateCheck:
		if active, err := m.hasActivePanelUpdateTask(nodeID); err != nil {
			return err
		} else if active {
			return ErrPanelUpdateActive
		}
		cancelable := true
		_, err := m.EnqueueTask(EnqueueTaskInput{NodeID: nodeID, BatchID: batch.ID, Type: TaskPanelUpdateCheck, Payload: json.RawMessage(`{}`), IdempotencyKey: batchTaskIdempotencyKey(batch.ID, nodeID, TaskPanelUpdateCheck), MaxAttempts: 1, RequestedBy: batch.RequestedBy, Cancelable: &cancelable, internal: true})
		return err
	case BatchActionUpdateApply:
		if active, err := m.hasActivePanelUpdateTask(nodeID); err != nil {
			return err
		} else if active {
			return ErrPanelUpdateActive
		}
		node, err := m.GetNode(nodeID)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(panelUpdateApplyPayload{ExpectedVersion: expectedVersion, CurrentVersion: node.PanelVersion})
		cancelable := true
		_, err = m.EnqueueTask(EnqueueTaskInput{NodeID: nodeID, BatchID: batch.ID, Type: TaskPanelUpdateApply, Payload: payload, IdempotencyKey: batchTaskIdempotencyKey(batch.ID, nodeID, TaskPanelUpdateApply), MaxAttempts: 1, RequestedBy: batch.RequestedBy, Cancelable: &cancelable, internal: true})
		return err
	case BatchActionDelete:
		task, err := m.createTerminalBatchTask(batch, nodeID, models.ClusterTaskStatusSucceeded, "")
		if err != nil {
			return err
		}
		if err := m.DeleteNode(nodeID); err != nil {
			now := time.Now()
			_ = m.db.Model(&task).Updates(map[string]any{"status": models.ClusterTaskStatusFailed, "stage": models.ClusterTaskStatusFailed, "error": boundedText(err.Error(), 1024), "finished_at": &now}).Error
			return err
		}
		return nil
	default:
		if !isLifecycleAction(batch.Action) {
			return ErrBatchAction
		}
		if _, err := m.ApplyLifecycle(nodeID, batch.Action); err != nil {
			return err
		}
		_, err := m.createTerminalBatchTask(batch, nodeID, models.ClusterTaskStatusSucceeded, "")
		return err
	}
}

func (m *Manager) createTerminalBatchTask(batch models.ClusterBatchOperation, nodeID uint, status, errorMessage string) (models.ClusterTask, error) {
	now := time.Now()
	taskType := batchTaskType(batch.Action)
	task := models.ClusterTask{
		NodeID: nodeID, BatchID: batch.ID, Type: taskType, IdempotencyKey: batchTaskIdempotencyKey(batch.ID, nodeID, taskType), Payload: `{}`,
		Status: status, Stage: status, Progress: 100, MaxAttempts: 1,
		RequestedBy: batch.RequestedBy, Cancelable: false, QueuedAt: now,
		StartedAt: &now, FinishedAt: &now, Error: boundedText(errorMessage, 1024),
	}
	if err := m.db.Create(&task).Error; err != nil {
		return models.ClusterTask{}, err
	}
	level, code, message := "info", "task_completed", "操作已完成"
	if status == models.ClusterTaskStatusFailed {
		level, code, message = "error", "task_failed", "操作执行失败"
	}
	_ = m.appendTaskEvent(task.ID, status, status, level, code, 100, message)
	return task, nil
}

func batchTaskType(action string) string {
	switch action {
	case BatchActionDiagnose:
		return TaskNodeDiagnose
	case BatchActionRestart:
		return "panel.restart"
	case BatchActionUpdateCheck:
		return TaskPanelUpdateCheck
	case BatchActionUpdateApply:
		return TaskPanelUpdateApply
	default:
		return "node." + action
	}
}

func batchTaskIdempotencyKey(batchID string, nodeID uint, taskType string) string {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return ""
	}
	return fmt.Sprintf("batch:%s:%d:%s", batchID, nodeID, strings.TrimSpace(taskType))
}

// RecoverMissingBatchTasks converts incomplete batch creation into explicit
// failed child tasks. The grace period prevents racing with a batch that is
// still creating its children.
func (m *Manager) RecoverMissingBatchTasks(grace time.Duration) (int, error) {
	if grace <= 0 {
		grace = 30 * time.Second
	}
	var batches []models.ClusterBatchOperation
	if err := m.db.Where("status IN ? AND created_at < ?", []string{models.ClusterTaskStatusQueued, models.ClusterTaskStatusRunning}, time.Now().Add(-grace)).Find(&batches).Error; err != nil {
		return 0, err
	}
	repaired := 0
	for _, batch := range batches {
		if batch.Action == serviceActionBatchAction {
			// Service-action preflights are workflow-linked rather than batch
			// children. Their terminal transition materializes the one counted
			// execution task for each node.
			continue
		}
		var tasks []models.ClusterTask
		if err := m.db.Where("batch_id = ?", batch.ID).Find(&tasks).Error; err != nil {
			return repaired, err
		}
		existing := make(map[uint]struct{}, len(tasks))
		for _, task := range tasks {
			existing[task.NodeID] = struct{}{}
		}
		batchRepaired := false
		for _, nodeID := range batch.NodeIDs {
			if _, ok := existing[nodeID]; ok {
				continue
			}
			if _, err := m.createTerminalBatchTask(batch, nodeID, models.ClusterTaskStatusFailed, "批次子任务创建失败，请重新发起操作"); err != nil {
				return repaired, err
			}
			repaired++
			batchRepaired = true
		}
		if batchRepaired {
			if err := m.updateBatchStatus(batch.ID); err != nil {
				return repaired, err
			}
		}
	}
	return repaired, nil
}

func (m *Manager) updateBatchStatus(batchID string) error {
	if strings.TrimSpace(batchID) == "" {
		return nil
	}
	var batch models.ClusterBatchOperation
	becameTerminal := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&batch, "id = ?", batchID).Error; err != nil {
			return err
		}
		values := map[string]int{}
		if batch.Action == serviceActionBatchAction {
			// A service action has two task phases per node. The execution phase
			// is authoritative when present; otherwise the preflight is the
			// node's current batch state. This keeps the batch total truthful
			// without counting the same node twice.
			var tasks []models.ClusterTask
			if err := tx.Where("batch_id = ? OR workflow_id = ?", batchID, batchID).Find(&tasks).Error; err != nil {
				return err
			}
			states := make(map[uint]string, len(batch.NodeIDs))
			for _, task := range tasks {
				if task.Type == TaskServiceActionPreflight {
					if _, hasExecution := states[task.NodeID]; !hasExecution {
						states[task.NodeID] = task.Status
					}
					continue
				}
				if task.Type == TaskServiceActionExecute {
					states[task.NodeID] = task.Status
				}
			}
			for _, nodeID := range batch.NodeIDs {
				status := states[nodeID]
				if status == "" {
					status = models.ClusterTaskStatusQueued
				}
				values[status]++
			}
		} else {
			type countRow struct {
				Status string
				Count  int
			}
			var counts []countRow
			if err := tx.Model(&models.ClusterTask{}).Select("status, count(*) as count").Where("batch_id = ?", batchID).Group("status").Scan(&counts).Error; err != nil {
				return err
			}
			for _, row := range counts {
				values[row.Status] = row.Count
			}
		}
		batch.Queued = values[models.ClusterTaskStatusQueued]
		batch.Running = values[models.ClusterTaskStatusRunning]
		batch.Succeeded = values[models.ClusterTaskStatusSucceeded]
		batch.Failed = values[models.ClusterTaskStatusFailed]
		batch.Canceled = values[models.ClusterTaskStatusCanceled]
		processed := batch.Succeeded + batch.Failed + batch.Canceled
		if processed >= batch.Total {
			becameTerminal = batch.FinishedAt == nil
			now := time.Now()
			batch.FinishedAt = &now
			switch {
			case batch.Failed > 0:
				batch.Status = models.ClusterTaskStatusFailed
			case batch.Canceled > 0:
				batch.Status = models.ClusterTaskStatusCanceled
			default:
				batch.Status = models.ClusterTaskStatusSucceeded
			}
		} else if batch.Running > 0 || processed > 0 {
			batch.Status = models.ClusterTaskStatusRunning
		} else {
			batch.Status = models.ClusterTaskStatusQueued
		}
		return tx.Save(&batch).Error
	})
	if err != nil || !becameTerminal {
		return err
	}
	if auditManager := auditservice.Default(); auditManager != nil {
		status := 200
		if batch.Status == models.ClusterTaskStatusFailed {
			status = 500
		}
		_, _ = auditManager.Append(auditservice.EventInput{
			EventType: "cluster", Action: "cluster.batch." + batch.Action,
			Status: status, Outcome: auditservice.OutcomeForStatus(status), Sensitive: true,
			UserID:    batch.RequestedBy,
			Message:   fmt.Sprintf("batch=%s result=%s total=%d succeeded=%d failed=%d canceled=%d", batch.ID, batch.Status, batch.Total, batch.Succeeded, batch.Failed, batch.Canceled),
			CreatedAt: time.Now().UTC(),
		})
	}
	return nil
}

func (m *Manager) ListBatches(page, pageSize int) (BatchList, error) {
	page, pageSize = normalizeTaskPage(page, pageSize)
	var total int64
	if err := m.db.Model(&models.ClusterBatchOperation{}).Count(&total).Error; err != nil {
		return BatchList{}, err
	}
	var items []models.ClusterBatchOperation
	if err := m.db.Order("created_at desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		return BatchList{}, err
	}
	return BatchList{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (m *Manager) GetBatch(id string) (BatchDetail, error) {
	var batch models.ClusterBatchOperation
	if err := m.db.First(&batch, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return BatchDetail{}, err
	}
	var tasks []models.ClusterTask
	if err := m.db.Where("batch_id = ? OR workflow_id = ?", batch.ID, batch.ID).Order("id asc").Find(&tasks).Error; err != nil {
		return BatchDetail{}, err
	}
	summaries := make([]ClusterTaskSummary, 0, len(tasks))
	for _, task := range tasks {
		summaries = append(summaries, SummarizeTask(task))
	}
	return BatchDetail{ClusterBatchOperation: batch, Tasks: summaries}, nil
}

func (m *Manager) CancelBatch(id string) (models.ClusterBatchOperation, error) {
	detail, err := m.GetBatch(id)
	if err != nil {
		return models.ClusterBatchOperation{}, err
	}
	// Validate the entire batch before mutating any queued task so a rejected
	// cancellation cannot leave the batch partially canceled.
	for _, task := range detail.Tasks {
		if task.Status != models.ClusterTaskStatusQueued && task.Status != models.ClusterTaskStatusRunning {
			continue
		}
		if !task.Cancelable {
			return models.ClusterBatchOperation{}, ErrTaskState
		}
		if task.Status == models.ClusterTaskStatusRunning {
			node, nodeErr := m.GetNode(task.NodeID)
			if nodeErr != nil || !hasCapability(node.Capabilities, CapabilityTaskCancel) {
				return models.ClusterBatchOperation{}, ErrTaskState
			}
		}
	}
	for _, task := range detail.Tasks {
		if task.Status == models.ClusterTaskStatusQueued || task.Status == models.ClusterTaskStatusRunning {
			_, cancelErr := m.CancelTask(task.NodeID, task.ID)
			if cancelErr != nil {
				return models.ClusterBatchOperation{}, cancelErr
			}
		}
	}
	_ = m.updateBatchStatus(id)
	var batch models.ClusterBatchOperation
	err = m.db.First(&batch, "id = ?", id).Error
	return batch, err
}

func validBatchAction(action string) bool {
	return action == BatchActionDiagnose || action == BatchActionRestart || action == BatchActionUpdateCheck || action == BatchActionUpdateApply || action == BatchActionDelete || isLifecycleAction(action)
}

func batchConcurrency(action string, policy models.ClusterPolicy) int {
	switch action {
	case BatchActionRestart:
		return policy.RestartConcurrency
	case BatchActionUpdateCheck, BatchActionUpdateApply:
		return policy.UpdateConcurrency
	case BatchActionDiagnose:
		return policy.DiagnosisConcurrency
	default:
		return policy.LifecycleConcurrency
	}
}

func batchImpact(action string, count int) (string, string) {
	switch action {
	case BatchActionRestart:
		return fmt.Sprintf("将依次重启 %d 个节点上的 Panel 服务，期间管理入口会短暂不可用", count), "RESTART NODES"
	case BatchActionUpdateApply:
		return fmt.Sprintf("将按检查结果更新 %d 个节点，更新阶段包含健康检查与失败回滚", count), "UPDATE NODES"
	case BatchActionDelete:
		return fmt.Sprintf("将软删除 %d 个节点并保留其任务、诊断、批次和审计历史", count), "DELETE NODES"
	case BatchActionDiagnose:
		return fmt.Sprintf("将对 %d 个节点执行只读诊断", count), ""
	case BatchActionUpdateCheck:
		return fmt.Sprintf("将检查 %d 个节点的 Panel 更新", count), ""
	default:
		return fmt.Sprintf("将修改 %d 个节点的生命周期状态", count), ""
	}
}

func normalizedNodeIDs(values []uint) []uint {
	seen := map[uint]struct{}{}
	result := make([]uint, 0, len(values))
	for _, value := range values {
		if value == 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
