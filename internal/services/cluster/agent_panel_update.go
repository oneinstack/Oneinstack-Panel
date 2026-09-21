package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/panelupdate"
)

const deferredPanelUpdateReceiptFile = "cluster-panel-update-task.json"

const (
	panelUpdateStartGrace     = 2 * time.Minute
	panelUpdateReceiptTimeout = 40 * time.Minute
)

var errPanelUpdateDeferred = errors.New("panel update completion is deferred until restart")

type deferredPanelUpdateReceipt struct {
	TaskID          uint64    `json:"taskId"`
	ExpectedVersion string    `json:"expectedVersion"`
	CreatedAt       time.Time `json:"createdAt"`
}

func (a *Agent) executePanelUpdateCheck(ctx context.Context) (json.RawMessage, error) {
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		return nil, errors.New(panelUpdateErrorFailed)
	}
	if needed, err := manager.NeedsRecovery(); err != nil {
		return nil, errors.New(panelUpdateErrorFailed)
	} else if needed {
		return nil, errors.New(panelUpdateErrorRecoveryRequired)
	}
	result, err := manager.Check(ctx)
	if err != nil && !errors.Is(err, panelupdate.ErrIncompatible) {
		return nil, errors.New(panelUpdateErrorCode(err))
	}
	checked := PanelUpdateCheckResult{
		CurrentVersion: result.CurrentVersion, LatestVersion: result.LatestVersion,
		UpdateAvailable: result.UpdateAvailable, Channel: result.Channel,
		ReleaseNotes: result.ReleaseNotes,
		Compatible:   result.Compatible, ArtifactSize: result.ArtifactSize,
		SigningKeyID: result.SigningKeyID, CheckedAt: time.Now().UTC(),
	}
	if !result.PublishedAt.IsZero() {
		publishedAt := result.PublishedAt
		checked.PublishedAt = &publishedAt
	}
	return json.Marshal(checked)
}

func (a *Agent) executePanelUpdateApply(ctx context.Context, task *models.ClusterTask) (json.RawMessage, error) {
	var payload panelUpdateApplyPayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || strings.TrimSpace(payload.ExpectedVersion) == "" {
		return nil, errors.New(panelUpdateErrorTargetChanged)
	}
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		return nil, errors.New(panelUpdateErrorFailed)
	}
	if needed, err := manager.NeedsRecovery(); err != nil {
		return nil, errors.New(panelUpdateErrorFailed)
	} else if needed {
		return nil, errors.New(panelUpdateErrorRecoveryRequired)
	}
	receipt := deferredPanelUpdateReceipt{
		TaskID: task.ID, ExpectedVersion: strings.TrimSpace(payload.ExpectedVersion), CreatedAt: time.Now().UTC(),
	}
	if err := writeDeferredPanelUpdateReceipt(receipt); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errors.New(panelUpdateErrorBusy)
		}
		return nil, errors.New(panelUpdateErrorFailed)
	}
	removeReceipt := true
	defer func() {
		if removeReceipt {
			_ = clearDeferredPanelUpdateReceipt()
		}
	}()
	if err := a.reportPanelUpdateProgress(ctx, task.ID, "checking", 45); err != nil {
		return nil, errors.New(panelUpdateErrorStartFailed)
	}
	if _, err := manager.QueueApplicationUpdate(ctx, payload.ExpectedVersion); err != nil {
		return nil, errors.New(panelUpdateErrorCode(err))
	}
	removeReceipt = false
	return nil, errPanelUpdateDeferred
}

// resumeDeferredPanelUpdate completes the controller task only after the new
// Panel process has registered again and the durable local update status is in
// a terminal state. The receipt intentionally contains no controller token.
func (a *Agent) resumeDeferredPanelUpdate(ctx context.Context) (bool, error) {
	receipt, found, err := readDeferredPanelUpdateReceipt()
	if err != nil {
		return true, err
	}
	if !found {
		return false, nil
	}
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		return true, err
	}
	status, err := manager.Status()
	if err != nil {
		return true, err
	}
	now := time.Now().UTC()
	statusBelongsToReceipt := status.StartedAt != nil && !status.StartedAt.Before(receipt.CreatedAt)
	if !statusBelongsToReceipt {
		if now.Before(receipt.CreatedAt.Add(panelUpdateStartGrace)) {
			if err := a.reportPanelUpdateProgress(ctx, receipt.TaskID, "starting_update", 45); err != nil {
				return true, err
			}
			return true, nil
		}
		return a.completeDeferredPanelUpdate(ctx, receipt, PanelUpdateExecutionResult{
			State: "failed", CurrentVersion: buildinfo.Version, TargetVersion: receipt.ExpectedVersion,
			ErrorCode: panelUpdateErrorStartFailed,
		}, models.ClusterTaskStatusFailed)
	}
	if panelUpdateStateActive(status.State) {
		if !now.Before(receipt.CreatedAt.Add(panelUpdateReceiptTimeout)) {
			return a.completeDeferredPanelUpdate(ctx, receipt, PanelUpdateExecutionResult{
				State: "failed", CurrentVersion: buildinfo.Version, TargetVersion: receipt.ExpectedVersion,
				RollbackAttempted: status.RollbackAttempted, RollbackSucceeded: status.RollbackSucceeded,
				ErrorCode: panelUpdateErrorResultUnknown,
			}, models.ClusterTaskStatusFailed)
		}
		if err := a.reportPanelUpdateProgress(ctx, receipt.TaskID, status.State, panelUpdateStateProgress(status.State)); err != nil {
			return true, err
		}
		return true, nil
	}

	result := PanelUpdateExecutionResult{
		State: status.State, CurrentVersion: buildinfo.Version,
		TargetVersion:     receipt.ExpectedVersion,
		RollbackAttempted: status.RollbackAttempted,
		RollbackSucceeded: status.RollbackSucceeded,
	}
	if status.State == panelupdate.StateSucceeded && exactPanelVersion(buildinfo.Version, receipt.ExpectedVersion) {
		return a.completeDeferredPanelUpdate(ctx, receipt, result, models.ClusterTaskStatusSucceeded)
	}
	result.ErrorCode = panelUpdateTerminalErrorCode(status)
	return a.completeDeferredPanelUpdate(ctx, receipt, result, models.ClusterTaskStatusFailed)
}

func (a *Agent) completeDeferredPanelUpdate(ctx context.Context, receipt deferredPanelUpdateReceipt, result PanelUpdateExecutionResult, terminalStatus string) (bool, error) {
	completion := TaskCompletion{TaskID: receipt.TaskID, Status: terminalStatus}
	if terminalStatus == models.ClusterTaskStatusFailed {
		completion.Error = result.ErrorCode
	}
	completion.Result, _ = json.Marshal(result)
	if err := a.post(ctx, "/cluster/agent/tasks/complete", completion, nil); err != nil {
		return true, err
	}
	if err := clearDeferredPanelUpdateReceipt(); err != nil {
		return true, err
	}
	return false, nil
}

func (a *Agent) reportPanelUpdateProgress(ctx context.Context, taskID uint64, stage string, progress int) error {
	return a.post(ctx, "/cluster/agent/tasks/progress", TaskProgressInput{
		TaskID: taskID, Stage: stage, Progress: progress, LeaseSeconds: 40 * 60,
	}, nil)
}

func deferredPanelUpdateReceiptPath() string {
	return filepath.Join(strings.TrimSuffix(app.GetBasePath(), "/"), "updates", deferredPanelUpdateReceiptFile)
}

func writeDeferredPanelUpdateReceipt(receipt deferredPanelUpdateReceipt) error {
	path := deferredPanelUpdateReceiptPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	content, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	content = append(content, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	remove = false
	return nil
}

func readDeferredPanelUpdateReceipt() (deferredPanelUpdateReceipt, bool, error) {
	content, err := os.ReadFile(deferredPanelUpdateReceiptPath())
	if errors.Is(err, os.ErrNotExist) {
		return deferredPanelUpdateReceipt{}, false, nil
	}
	if err != nil {
		return deferredPanelUpdateReceipt{}, false, err
	}
	var receipt deferredPanelUpdateReceipt
	if err := json.Unmarshal(content, &receipt); err != nil {
		return deferredPanelUpdateReceipt{}, true, fmt.Errorf("decode deferred panel update receipt: %w", err)
	}
	if receipt.TaskID == 0 || strings.TrimSpace(receipt.ExpectedVersion) == "" {
		return deferredPanelUpdateReceipt{}, true, errors.New("deferred panel update receipt is invalid")
	}
	return receipt, true, nil
}

func clearDeferredPanelUpdateReceipt() error {
	err := os.Remove(deferredPanelUpdateReceiptPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func panelUpdateStateActive(state string) bool {
	switch state {
	case panelupdate.StateChecking, panelupdate.StateDownloading, panelupdate.StatePreflight,
		panelupdate.StateSwitching, panelupdate.StateHealthChecking:
		return true
	default:
		return false
	}
}

func panelUpdateStateProgress(state string) int {
	switch state {
	case panelupdate.StateChecking:
		return 50
	case panelupdate.StateDownloading:
		return 60
	case panelupdate.StatePreflight:
		return 70
	case panelupdate.StateSwitching:
		return 80
	case panelupdate.StateHealthChecking:
		return 90
	default:
		return 45
	}
}

func panelUpdateErrorCode(err error) string {
	switch {
	case errors.Is(err, panelupdate.ErrDisabled):
		return panelUpdateErrorDisabled
	case errors.Is(err, panelupdate.ErrNoUpdate):
		return panelUpdateErrorNoUpdate
	case errors.Is(err, panelupdate.ErrUpdateBusy), errors.Is(err, os.ErrExist):
		return panelUpdateErrorBusy
	case errors.Is(err, panelupdate.ErrRecoveryNeeded):
		return panelUpdateErrorRecoveryRequired
	case errors.Is(err, panelupdate.ErrUpdateStart):
		return panelUpdateErrorStartFailed
	case errors.Is(err, panelupdate.ErrIncompatible):
		return panelUpdateErrorIncompatible
	case errors.Is(err, panelupdate.ErrTargetChanged):
		return panelUpdateErrorTargetChanged
	case errors.Is(err, panelupdate.ErrCenterTimeout):
		return panelUpdateErrorCenterTimeout
	case errors.Is(err, panelupdate.ErrCenterUnavailable):
		return panelUpdateErrorCenterUnavailable
	case errors.Is(err, panelupdate.ErrDownloadFailed):
		return panelUpdateErrorDownloadFailed
	case errors.Is(err, panelupdate.ErrVerificationFailed), errors.Is(err, panelupdate.ErrInvalidManifest):
		return panelUpdateErrorVerificationFailed
	case errors.Is(err, panelupdate.ErrPreflightFailed):
		return panelUpdateErrorPreflightFailed
	case errors.Is(err, panelupdate.ErrServiceFailed):
		return panelUpdateErrorServiceFailed
	case errors.Is(err, panelupdate.ErrHealthCheckFailed):
		return panelUpdateErrorHealthCheckFailed
	default:
		return panelUpdateErrorFailed
	}
}

func panelUpdateTerminalErrorCode(status panelupdate.Status) string {
	switch status.State {
	case panelupdate.StateRolledBack:
		if code := panelUpdateStatusErrorCode(status.ErrorCode); code != "" {
			return code
		}
		return panelUpdateErrorRolledBack
	case panelupdate.StateRollbackFailed:
		return panelUpdateErrorRollbackFailed
	case panelupdate.StateRecoveryNeeded:
		return panelUpdateErrorRecoveryRequired
	case panelupdate.StateFailed:
		if code := panelUpdateStatusErrorCode(status.ErrorCode); code != "" {
			return code
		}
		return panelUpdateErrorFailed
	default:
		return panelUpdateErrorResultUnknown
	}
}

func panelUpdateStatusErrorCode(code string) string {
	switch code {
	case panelupdate.StatusErrorNoUpdate:
		return panelUpdateErrorNoUpdate
	case panelupdate.StatusErrorIncompatible:
		return panelUpdateErrorIncompatible
	case panelupdate.StatusErrorTargetChanged:
		return panelUpdateErrorTargetChanged
	case panelupdate.StatusErrorCenterTimeout:
		return panelUpdateErrorCenterTimeout
	case panelupdate.StatusErrorCenterUnavailable:
		return panelUpdateErrorCenterUnavailable
	case panelupdate.StatusErrorDownloadFailed:
		return panelUpdateErrorDownloadFailed
	case panelupdate.StatusErrorVerificationFailed:
		return panelUpdateErrorVerificationFailed
	case panelupdate.StatusErrorPreflightFailed:
		return panelUpdateErrorPreflightFailed
	case panelupdate.StatusErrorServiceFailed:
		return panelUpdateErrorServiceFailed
	case panelupdate.StatusErrorHealthCheckFailed:
		return panelUpdateErrorHealthCheckFailed
	default:
		return ""
	}
}
