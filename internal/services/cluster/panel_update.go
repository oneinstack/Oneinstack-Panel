package cluster

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"

	"golang.org/x/mod/semver"
	"gorm.io/gorm"
)

const (
	TaskPanelUpdateCheck = "panel.update.check"
	TaskPanelUpdateApply = "panel.update.apply"

	CapabilityPanelUpdateCheck = TaskPanelUpdateCheck
	CapabilityPanelUpdateApply = TaskPanelUpdateApply

	panelUpdateConfirmation = "UPDATE PANEL"

	panelUpdateErrorDisabled         = "PANEL_UPDATE_DISABLED"
	panelUpdateErrorNoUpdate         = "PANEL_UPDATE_NO_UPDATE"
	panelUpdateErrorIncompatible     = "PANEL_UPDATE_INCOMPATIBLE"
	panelUpdateErrorTargetChanged    = "PANEL_UPDATE_TARGET_CHANGED"
	panelUpdateErrorBusy             = "PANEL_UPDATE_BUSY"
	panelUpdateErrorRecoveryRequired = "PANEL_UPDATE_RECOVERY_REQUIRED"
	panelUpdateErrorStartFailed      = "PANEL_UPDATE_START_FAILED"
	panelUpdateErrorRolledBack       = "PANEL_UPDATE_ROLLED_BACK"
	panelUpdateErrorRollbackFailed   = "PANEL_UPDATE_ROLLBACK_FAILED"
	panelUpdateErrorFailed           = "PANEL_UPDATE_FAILED"
	panelUpdateErrorResultUnknown    = "PANEL_UPDATE_RESULT_UNKNOWN"
)

var (
	ErrPanelUpdateCapability = errors.New("node does not support panel update tasks")
	ErrPanelUpdateActive     = errors.New("node already has an active panel update task")
	ErrPanelUpdateTarget     = errors.New("panel update target is invalid or stale")
	ErrPanelUpdateConfirm    = errors.New("panel update confirmation is invalid")
	panelUpdateEnqueueMu     sync.Mutex
)

type PanelUpdateCheckResult struct {
	CurrentVersion  string     `json:"currentVersion"`
	LatestVersion   string     `json:"latestVersion,omitempty"`
	UpdateAvailable bool       `json:"updateAvailable"`
	Channel         string     `json:"channel"`
	PublishedAt     *time.Time `json:"publishedAt,omitempty"`
	ReleaseNotes    string     `json:"releaseNotes,omitempty"`
	Compatible      bool       `json:"compatible"`
	ArtifactSize    int64      `json:"artifactSize,omitempty"`
	SigningKeyID    string     `json:"signingKeyId,omitempty"`
	CheckedAt       time.Time  `json:"checkedAt"`
}

type PanelUpdateExecutionResult struct {
	State             string `json:"state"`
	CurrentVersion    string `json:"currentVersion,omitempty"`
	TargetVersion     string `json:"targetVersion,omitempty"`
	RollbackAttempted bool   `json:"rollbackAttempted"`
	RollbackSucceeded bool   `json:"rollbackSucceeded"`
	ErrorCode         string `json:"errorCode,omitempty"`
}

type PanelUpdateState struct {
	NodeID         uint                        `json:"nodeId"`
	CurrentVersion string                      `json:"currentVersion"`
	CanCheck       bool                        `json:"canCheck"`
	CanApply       bool                        `json:"canApply"`
	LastCheck      *PanelUpdateCheckResult     `json:"lastCheck,omitempty"`
	LastExecution  *PanelUpdateExecutionResult `json:"lastExecution,omitempty"`
	ActiveTask     *ClusterTaskSummary         `json:"activeTask,omitempty"`
	LastTask       *ClusterTaskSummary         `json:"lastTask,omitempty"`
}

type PanelUpdateApplyInput struct {
	ExpectedVersion string `json:"expectedVersion"`
	Confirm         string `json:"confirm"`
}

type panelUpdateApplyPayload struct {
	ExpectedVersion string `json:"expectedVersion"`
	CurrentVersion  string `json:"currentVersion"`
}

func PanelUpdateCapabilities() []string {
	return []string{CapabilityPanelUpdateCheck, CapabilityPanelUpdateApply, CapabilityNodeDiagnose, CapabilityTaskCancel}
}

func isPanelUpdateTask(taskType string) bool {
	return taskType == TaskPanelUpdateCheck || taskType == TaskPanelUpdateApply
}

func safePanelUpdateErrorCode(value string) string {
	value = strings.TrimSpace(value)
	switch value {
	case panelUpdateErrorDisabled,
		panelUpdateErrorNoUpdate,
		panelUpdateErrorIncompatible,
		panelUpdateErrorTargetChanged,
		panelUpdateErrorBusy,
		panelUpdateErrorRecoveryRequired,
		panelUpdateErrorStartFailed,
		panelUpdateErrorRolledBack,
		panelUpdateErrorRollbackFailed,
		panelUpdateErrorFailed,
		panelUpdateErrorResultUnknown:
		return value
	default:
		return ""
	}
}

func (m *Manager) GetPanelUpdateState(id uint) (PanelUpdateState, error) {
	states, err := m.GetPanelUpdateStates([]uint{id})
	if err != nil {
		return PanelUpdateState{}, err
	}
	if len(states) == 0 {
		return PanelUpdateState{}, gorm.ErrRecordNotFound
	}
	return states[0], nil
}

// GetPanelUpdateStates keeps list-page refreshes on one HTTP request. Missing
// nodes are ignored because they may have been soft-deleted between the node
// list request and this status refresh.
func (m *Manager) GetPanelUpdateStates(ids []uint) ([]PanelUpdateState, error) {
	if len(ids) == 0 {
		return []PanelUpdateState{}, nil
	}
	var nodes []models.ClusterNode
	if err := m.db.Where("id IN ?", ids).Find(&nodes).Error; err != nil {
		return nil, err
	}
	statesByNode := make(map[uint]*PanelUpdateState, len(nodes))
	for i := range nodes {
		node := nodes[i]
		statesByNode[node.ID] = &PanelUpdateState{
			NodeID: node.ID, CurrentVersion: node.PanelVersion,
			CanCheck: nodeHasCapability(node, CapabilityPanelUpdateCheck),
			CanApply: nodeHasCapability(node, CapabilityPanelUpdateApply),
		}
	}

	panelTypes := []string{TaskPanelUpdateCheck, TaskPanelUpdateApply}
	lastSubquery := m.db.Model(&models.ClusterTask{}).
		Select("MAX(id)").
		Where("node_id IN ? AND type IN ?", ids, panelTypes).
		Group("node_id")
	var lastTasks []models.ClusterTask
	if err := m.db.Where("id IN (?)", lastSubquery).Find(&lastTasks).Error; err != nil {
		return nil, err
	}
	for i := range lastTasks {
		state := statesByNode[lastTasks[i].NodeID]
		if state == nil {
			continue
		}
		summary := SummarizeTask(lastTasks[i])
		state.LastTask = &summary
		if lastTasks[i].Status == models.ClusterTaskStatusQueued || lastTasks[i].Status == models.ClusterTaskStatusRunning {
			state.ActiveTask = &summary
		}
	}

	applySubquery := m.db.Model(&models.ClusterTask{}).
		Select("MAX(id)").
		Where("node_id IN ? AND type = ?", ids, TaskPanelUpdateApply).
		Group("node_id")
	var executionTasks []models.ClusterTask
	if err := m.db.Where("id IN (?)", applySubquery).Find(&executionTasks).Error; err != nil {
		return nil, err
	}
	lastExecutionUpdatedAt := make(map[uint]time.Time, len(executionTasks))
	for i := range executionTasks {
		state := statesByNode[executionTasks[i].NodeID]
		if state == nil || strings.TrimSpace(executionTasks[i].Result) == "" {
			continue
		}
		lastExecutionUpdatedAt[executionTasks[i].NodeID] = executionTasks[i].UpdatedAt
		var execution PanelUpdateExecutionResult
		if json.Unmarshal([]byte(executionTasks[i].Result), &execution) == nil {
			execution.ErrorCode = safePanelUpdateErrorCode(execution.ErrorCode)
			state.LastExecution = &execution
		}
	}

	checkSubquery := m.db.Model(&models.ClusterTask{}).
		Select("MAX(id)").
		Where("node_id IN ? AND type = ? AND status = ?", ids, TaskPanelUpdateCheck, models.ClusterTaskStatusSucceeded).
		Group("node_id")
	var checkTasks []models.ClusterTask
	if err := m.db.Where("id IN (?)", checkSubquery).Find(&checkTasks).Error; err != nil {
		return nil, err
	}
	for i := range checkTasks {
		state := statesByNode[checkTasks[i].NodeID]
		if state == nil || strings.TrimSpace(checkTasks[i].Result) == "" {
			continue
		}
		var check PanelUpdateCheckResult
		if json.Unmarshal([]byte(checkTasks[i].Result), &check) == nil {
			// The node heartbeat is the live source of truth. The version in a
			// check result is only a snapshot and becomes stale after a
			// successful update.
			currentVersion := strings.TrimSpace(state.CurrentVersion)
			if currentVersion == "" {
				currentVersion = strings.TrimSpace(check.CurrentVersion)
				state.CurrentVersion = currentVersion
			}
			check.UpdateAvailable = panelVersionUpdateAvailable(currentVersion, check.LatestVersion)
			state.LastCheck = &check

			// A successful check supersedes an older failed apply result. Keep a
			// current success result visible, but do not retain a stale error in
			// the panel update state.
			if executionUpdatedAt, ok := lastExecutionUpdatedAt[checkTasks[i].NodeID]; ok &&
				!checkTasks[i].UpdatedAt.Before(executionUpdatedAt) &&
				state.LastExecution != nil && state.LastExecution.ErrorCode != "" {
				state.LastExecution = nil
			}
		}
	}

	states := make([]PanelUpdateState, 0, len(statesByNode))
	for _, id := range ids {
		if state := statesByNode[id]; state != nil {
			states = append(states, *state)
		}
	}
	return states, nil
}

func (m *Manager) EnqueuePanelUpdateCheck(id uint) (ClusterTaskSummary, error) {
	node, err := m.panelUpdateNode(id, CapabilityPanelUpdateCheck)
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	panelUpdateEnqueueMu.Lock()
	defer panelUpdateEnqueueMu.Unlock()
	if active, err := m.hasActivePanelUpdateTask(node.ID); err != nil {
		return ClusterTaskSummary{}, err
	} else if active {
		return ClusterTaskSummary{}, ErrPanelUpdateActive
	}
	task, err := m.EnqueueTask(EnqueueTaskInput{
		NodeID:         node.ID,
		Type:           TaskPanelUpdateCheck,
		Payload:        json.RawMessage(`{}`),
		MaxAttempts:    1,
		IdempotencyKey: fmt.Sprintf("panel-update-check:%d:%d", node.ID, time.Now().UTC().UnixNano()),
		internal:       true,
	})
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	return SummarizeTask(task), nil
}

func (m *Manager) EnqueuePanelUpdateApply(id uint, input PanelUpdateApplyInput) (ClusterTaskSummary, error) {
	if input.Confirm != panelUpdateConfirmation {
		return ClusterTaskSummary{}, ErrPanelUpdateConfirm
	}
	node, err := m.panelUpdateNode(id, CapabilityPanelUpdateApply)
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	panelUpdateEnqueueMu.Lock()
	defer panelUpdateEnqueueMu.Unlock()
	if active, err := m.hasActivePanelUpdateTask(node.ID); err != nil {
		return ClusterTaskSummary{}, err
	} else if active {
		return ClusterTaskSummary{}, ErrPanelUpdateActive
	}
	state, err := m.GetPanelUpdateState(node.ID)
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	target := strings.TrimSpace(input.ExpectedVersion)
	now := time.Now().UTC()
	checkFresh := state.LastCheck != nil && !state.LastCheck.CheckedAt.IsZero() &&
		!now.After(state.LastCheck.CheckedAt.Add(panelUpdateCheckInterval))
	if !checkFresh || !state.LastCheck.UpdateAvailable || !state.LastCheck.Compatible || !exactPanelVersion(target, state.LastCheck.LatestVersion) {
		return ClusterTaskSummary{}, ErrPanelUpdateTarget
	}
	payload, _ := json.Marshal(panelUpdateApplyPayload{ExpectedVersion: target, CurrentVersion: node.PanelVersion})
	task, err := m.EnqueueTask(EnqueueTaskInput{
		NodeID:         node.ID,
		Type:           TaskPanelUpdateApply,
		Payload:        payload,
		MaxAttempts:    1,
		IdempotencyKey: fmt.Sprintf("panel-update-apply:%d:%s:%d", node.ID, strings.TrimPrefix(target, "v"), time.Now().UTC().UnixNano()),
		internal:       true,
	})
	if err != nil {
		return ClusterTaskSummary{}, err
	}
	recordPanelUpdateAudit(task, "queued")
	return SummarizeTask(task), nil
}

func (m *Manager) panelUpdateNode(id uint, capability string) (models.ClusterNode, error) {
	node, err := m.GetNode(id)
	if err != nil {
		return node, err
	}
	if !nodeHeartbeatFresh(node, time.Now()) {
		return node, ErrNodeUnavailable
	}
	if !nodeHasCapability(node, capability) {
		return node, ErrPanelUpdateCapability
	}
	return node, nil
}

func (m *Manager) hasActivePanelUpdateTask(nodeID uint) (bool, error) {
	var count int64
	err := m.db.Model(&models.ClusterTask{}).
		Where("node_id = ? AND type IN ? AND status IN ?", nodeID,
			[]string{TaskPanelUpdateCheck, TaskPanelUpdateApply},
			[]string{models.ClusterTaskStatusQueued, models.ClusterTaskStatusRunning}).
		Count(&count).Error
	return count > 0, err
}

func panelVersionUpdateAvailable(current, latest string) bool {
	current = canonicalPanelVersion(current)
	latest = canonicalPanelVersion(latest)
	return current != "" && latest != "" && semver.Compare(latest, current) > 0
}

func canonicalPanelVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.TrimPrefix(strings.ToLower(value), "v")
	value = "v" + value
	if !semver.IsValid(value) {
		return ""
	}
	return value
}

func exactPanelVersion(left, right string) bool {
	leftCanonical := canonicalPanelVersion(left)
	rightCanonical := canonicalPanelVersion(right)
	if leftCanonical != "" && rightCanonical != "" {
		return leftCanonical == rightCanonical
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && left == right
}

func validPanelUpdateProgressStage(stage string) bool {
	switch stage {
	case "starting_update", "checking", "downloading", "preflight", "switching", "health_checking":
		return true
	default:
		return false
	}
}

func sanitizePanelUpdateCompletion(node models.ClusterNode, task models.ClusterTask, input TaskCompletion) (TaskCompletion, error) {
	input.Error = safePanelUpdateErrorCode(input.Error)
	if input.Status == models.ClusterTaskStatusFailed && input.Error == "" {
		input.Error = panelUpdateErrorFailed
	}
	switch task.Type {
	case TaskPanelUpdateCheck:
		if input.Status != models.ClusterTaskStatusSucceeded {
			input.Result = nil
			return input, nil
		}
		var result PanelUpdateCheckResult
		if err := json.Unmarshal(input.Result, &result); err != nil {
			return input, ErrTaskState
		}
		result.CurrentVersion = boundedText(result.CurrentVersion, 120)
		result.LatestVersion = boundedText(result.LatestVersion, 120)
		result.Channel = boundedText(result.Channel, 32)
		result.ReleaseNotes = boundedText(result.ReleaseNotes, 16<<10)
		result.SigningKeyID = boundedText(result.SigningKeyID, 120)
		if result.CurrentVersion == "" || !validPanelUpdateChannel(result.Channel) || result.CheckedAt.IsZero() || result.ArtifactSize < 0 || (result.UpdateAvailable && result.LatestVersion == "") {
			return input, ErrTaskState
		}
		input.Result, _ = json.Marshal(result)
	case TaskPanelUpdateApply:
		var payload panelUpdateApplyPayload
		if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || strings.TrimSpace(payload.ExpectedVersion) == "" {
			return input, ErrTaskState
		}
		var result PanelUpdateExecutionResult
		if len(input.Result) == 0 && input.Status == models.ClusterTaskStatusFailed {
			result = PanelUpdateExecutionResult{
				State:          "failed",
				CurrentVersion: node.PanelVersion,
				TargetVersion:  payload.ExpectedVersion,
				ErrorCode:      input.Error,
			}
		} else if err := json.Unmarshal(input.Result, &result); err != nil {
			return input, ErrTaskState
		}
		result.State = boundedText(result.State, 64)
		result.CurrentVersion = boundedText(result.CurrentVersion, 120)
		result.TargetVersion = boundedText(result.TargetVersion, 120)
		result.ErrorCode = safePanelUpdateErrorCode(result.ErrorCode)
		if !exactPanelVersion(result.TargetVersion, payload.ExpectedVersion) {
			return input, ErrTaskState
		}
		if input.Status == models.ClusterTaskStatusSucceeded {
			registeredAfterStart := node.LastRegisteredAt != nil && task.StartedAt != nil && node.LastRegisteredAt.After(*task.StartedAt)
			if result.State != "succeeded" || !exactPanelVersion(result.CurrentVersion, result.TargetVersion) || !exactPanelVersion(node.PanelVersion, result.TargetVersion) || !nodeHeartbeatFresh(node, time.Now()) || !registeredAfterStart {
				return input, ErrTaskState
			}
		} else {
			result.State = safeFailedPanelUpdateState(result.State)
			if result.ErrorCode == "" {
				result.ErrorCode = input.Error
			}
		}
		input.Error = result.ErrorCode
		input.Result, _ = json.Marshal(result)
	default:
		return input, ErrTaskState
	}
	return input, nil
}

func validPanelUpdateChannel(channel string) bool {
	switch channel {
	case "stable", "beta", "development":
		return true
	default:
		return false
	}
}

func safeFailedPanelUpdateState(state string) string {
	switch state {
	case "failed", "rolled_back", "rollback_failed", "recovery_required":
		return state
	default:
		return "failed"
	}
}

func boundedText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit {
		return value[:limit]
	}
	return value
}

func recordPanelUpdateAudit(task models.ClusterTask, outcome string) {
	if task.Type != TaskPanelUpdateApply {
		return
	}
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	var payload panelUpdateApplyPayload
	_ = json.Unmarshal([]byte(task.Payload), &payload)
	status := 202
	if outcome != "queued" {
		status = 200
	}
	message := fmt.Sprintf(
		"node=%d from=%s target=%s task=%d result=%s",
		task.NodeID,
		boundedText(payload.CurrentVersion, 120),
		boundedText(payload.ExpectedVersion, 120),
		task.ID,
		boundedText(task.Status, 16),
	)
	if code := safePanelUpdateErrorCode(task.Error); code != "" {
		message += " error_code=" + code
	}
	_, _ = manager.Append(auditservice.EventInput{
		EventType: "cluster", Action: "cluster.panel_update.apply", Status: status,
		Outcome: outcome, Sensitive: true, Message: message, CreatedAt: time.Now(),
	})
}
