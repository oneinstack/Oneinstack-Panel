package softwaretask

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type phaseRange struct {
	status string
	start  int
	end    int
	label  string
}

var softwareTaskPhases = map[string]phaseRange{
	"resolving": {
		status: models.SoftwareTaskStatusResolving,
		start:  0,
		end:    5,
		label:  "获取安装包",
	},
	"precheck": {
		status: models.SoftwareTaskStatusPrechecking,
		start:  5,
		end:    12,
		label:  "环境预检",
	},
	"install": {
		status: models.SoftwareTaskStatusInstalling,
		start:  12,
		end:    72,
		label:  "安装软件",
	},
	"upgrade": {
		status: models.SoftwareTaskStatusUpgrading,
		start:  12,
		end:    72,
		label:  "升级软件",
	},
	"uninstall": {
		status: models.SoftwareTaskStatusUninstalling,
		start:  5,
		end:    96,
		label:  "卸载软件",
	},
	"start": {
		status: models.SoftwareTaskStatusStarting,
		start:  5,
		end:    96,
		label:  "启动服务",
	},
	"stop": {
		status: models.SoftwareTaskStatusStopping,
		start:  5,
		end:    96,
		label:  "停止服务",
	},
	"restart": {
		status: models.SoftwareTaskStatusRestarting,
		start:  5,
		end:    96,
		label:  "重启服务",
	},
	"reload": {
		status: models.SoftwareTaskStatusReloading,
		start:  5,
		end:    96,
		label:  "重载服务",
	},
	"configure": {
		status: models.SoftwareTaskStatusConfiguring,
		start:  72,
		end:    84,
		label:  "写入配置",
	},
	"config_apply": {
		status: models.SoftwareTaskStatusConfiguring,
		start:  5,
		end:    96,
		label:  "安全发布组件配置",
	},
	"verify": {
		status: models.SoftwareTaskStatusVerifying,
		start:  84,
		end:    96,
		label:  "启动并验证",
	},
	"finalizing": {
		status: models.SoftwareTaskStatusFinalizing,
		start:  96,
		end:    100,
		label:  "保存任务状态",
	},
}

// Reporter implements the script execution observer without making the script
// package depend on task persistence.
type Reporter struct {
	manager *Manager
	taskID  string
	mu      *sync.Mutex
}

func newReporter(manager *Manager, taskID string) *Reporter {
	value, _ := manager.eventLocks.LoadOrStore(taskID, &sync.Mutex{})
	return &Reporter{manager: manager, taskID: taskID, mu: value.(*sync.Mutex)}
}

func (r *Reporter) OnActionStart(action string) {
	phase, exists := normalizeActionPhase(action)
	if !exists {
		return
	}
	spec := softwareTaskPhases[phase]
	_ = r.setPhase(phase, nil, "正在"+spec.label, action+"_started")
}

func (r *Reporter) OnPackageResolved(version, source string) {
	version = strings.TrimSpace(version)
	source = strings.TrimSpace(source)
	if version == "" && source == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	message := "已解析组件脚本包"
	if version != "" {
		message += " " + version
	}
	if source != "" {
		message += "（" + source + "）"
	}
	_ = r.publishLocked(taskUpdate{
		resolvedVersion: version,
		packageSource:   source,
	}, eventData{
		eventType: "package",
		level:     "info",
		code:      "package_resolved",
		message:   message,
	})
}

func (r *Reporter) OnActionProgress(action string, percent int, code, message string) {
	phase, exists := normalizeActionPhase(action)
	if !exists {
		return
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	if strings.TrimSpace(message) == "" {
		message = "正在" + softwareTaskPhases[phase].label
	}
	_ = r.setPhase(phase, intPointer(percent), message, code)
}

func (r *Reporter) OnActionComplete(action string) {
	phase, exists := normalizeActionPhase(action)
	if !exists {
		return
	}
	spec := softwareTaskPhases[phase]
	_ = r.setPhase(phase, intPointer(100), spec.label+"完成", action+"_completed")
}

func (r *Reporter) OnRollbackStart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	_ = r.publishLocked(taskUpdate{
		status:         models.SoftwareTaskStatusRollback,
		phase:          models.SoftwareTaskStatusRollback,
		message:        "安装失败，正在回滚变更",
		rollbackStatus: models.SoftwareTaskRollbackRunning,
	}, eventData{
		eventType: "phase",
		level:     "warning",
		code:      "rollback_started",
		message:   "安装失败，正在回滚变更",
	})
}

func (r *Reporter) OnRollbackComplete(rollbackErr error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := models.SoftwareTaskRollbackSucceeded
	level := "warning"
	code := "rollback_completed"
	message := "回滚完成"
	if rollbackErr != nil {
		status = models.SoftwareTaskRollbackFailed
		level = "error"
		code = "rollback_failed"
		message = "回滚失败：" + safeErrorMessage(rollbackErr)
	}
	update := taskUpdate{
		message:        message,
		rollbackStatus: status,
	}
	if rollbackErr != nil {
		update.recoveryStatus = "recovery_required"
		update.recoveryMessage = "自动回滚失败；已保留迁移快照，请根据任务日志执行恢复"
	}
	_ = r.publishLocked(update, eventData{
		eventType: "warning",
		level:     level,
		code:      code,
		message:   message,
	})
}

func (r *Reporter) setPhase(phase string, phaseProgress *int, message, code string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, exists := softwareTaskPhases[phase]
	if !exists {
		return fmt.Errorf("unknown software task phase %s", phase)
	}
	progress := spec.start
	if phaseProgress != nil {
		progress = spec.start + ((spec.end - spec.start) * *phaseProgress / 100)
	}
	return r.publishLocked(taskUpdate{
		status:        spec.status,
		phase:         phase,
		phaseProgress: phaseProgress,
		progress:      &progress,
		message:       message,
		startTask:     true,
	}, eventData{
		eventType:     eventTypeForProgress(phaseProgress),
		level:         "info",
		phaseProgress: phaseProgress,
		code:          code,
		message:       message,
	})
}

func (r *Reporter) finish(status, errorCode, message string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	progress := 100
	level := "info"
	if status != models.SoftwareTaskStatusSucceeded {
		progress = -1
		level = "error"
	}
	update := taskUpdate{
		status:     status,
		phase:      status,
		message:    message,
		errorCode:  errorCode,
		finishedAt: &now,
	}
	if progress >= 0 {
		update.progress = &progress
		update.phaseProgress = intPointer(100)
	}
	if status == models.SoftwareTaskStatusFailed || status == models.SoftwareTaskStatusCanceled {
		update.errorMessage = message
		update.setFailurePhase = true
	}
	err := r.publishLocked(update, eventData{
		eventType: "terminal",
		level:     level,
		code:      errorCode,
		message:   message,
	})
	if err != nil {
		return err
	}
	if status != models.SoftwareTaskStatusSucceeded {
		return r.manager.clearTaskSecret(r.taskID)
	}
	return nil
}

type taskUpdate struct {
	status          string
	phase           string
	phaseProgress   *int
	progress        *int
	message         string
	errorCode       string
	errorMessage    string
	rollbackStatus  string
	recoveryStatus  string
	recoveryMessage string
	startTask       bool
	finishedAt      *time.Time
	setFailurePhase bool
	cancelRequested *bool
	resolvedVersion string
	packageSource   string
}

type eventData struct {
	eventType     string
	level         string
	phaseProgress *int
	code          string
	message       string
}

func (r *Reporter) publishLocked(update taskUpdate, event eventData) error {
	err := r.manager.db.Transaction(func(tx *gorm.DB) error {
		var task models.SoftwareTask
		if err := tx.First(&task, "id = ?", r.taskID).Error; err != nil {
			return err
		}
		if models.IsSoftwareTaskTerminal(task.Status) {
			return nil
		}
		previousPhase := task.Phase
		if update.status != "" {
			task.Status = update.status
		}
		if update.phase != "" {
			task.Phase = update.phase
		}
		if update.phaseProgress != nil || update.phase != "" {
			task.PhaseProgress = update.phaseProgress
		}
		if update.progress != nil && *update.progress >= task.Progress {
			task.Progress = *update.progress
		}
		if update.message != "" {
			task.Message = update.message
		}
		if update.errorCode != "" {
			task.ErrorCode = update.errorCode
		}
		if update.errorMessage != "" {
			task.ErrorMessage = update.errorMessage
		}
		if update.rollbackStatus != "" {
			task.RollbackStatus = update.rollbackStatus
		}
		if update.recoveryStatus != "" {
			task.RecoveryStatus = update.recoveryStatus
		}
		if update.recoveryMessage != "" {
			task.RecoveryMessage = update.recoveryMessage
		}
		if update.startTask && task.StartedAt == nil {
			now := time.Now()
			task.StartedAt = &now
		}
		now := time.Now()
		task.HeartbeatAt = &now
		if update.finishedAt != nil {
			task.FinishedAt = update.finishedAt
		}
		if update.setFailurePhase {
			task.FailurePhase = previousPhase
		}
		if update.cancelRequested != nil {
			task.CancelRequested = *update.cancelRequested
		}
		if update.resolvedVersion != "" {
			task.ResolvedVersion = update.resolvedVersion
		}
		if update.packageSource != "" {
			task.PackageSource = update.packageSource
		}
		task.EventSeq++
		if err := tx.Save(&task).Error; err != nil {
			return err
		}
		if update.finishedAt != nil && task.Operation == "configure" {
			historyStatus := configurationHistoryStatus(task.Status)
			if err := tx.Model(&models.SoftwareConfigurationHistory{}).
				Where("task_id = ? AND status = ?", task.ID, models.SoftwareConfigurationStatusPending).
				Updates(map[string]any{
					"status":      historyStatus,
					"finished_at": update.finishedAt,
					"updated_at":  now,
				}).Error; err != nil {
				return err
			}
		}
		taskEvent := models.SoftwareTaskEvent{
			TaskID:        task.ID,
			Seq:           task.EventSeq,
			Type:          event.eventType,
			Level:         event.level,
			Status:        task.Status,
			Phase:         task.Phase,
			PhaseProgress: event.phaseProgress,
			Progress:      task.Progress,
			Code:          event.code,
			Message:       event.message,
			CreatedAt:     now,
		}
		return tx.Create(&taskEvent).Error
	})
	if err == nil {
		r.manager.notify(r.taskID)
	}
	return err
}

func configurationHistoryStatus(taskStatus string) string {
	switch taskStatus {
	case models.SoftwareTaskStatusSucceeded:
		return models.SoftwareConfigurationStatusSucceeded
	case models.SoftwareTaskStatusCanceled:
		return models.SoftwareConfigurationStatusCanceled
	case models.SoftwareTaskStatusInterrupted:
		return models.SoftwareConfigurationStatusInterrupted
	default:
		return models.SoftwareConfigurationStatusFailed
	}
}

func normalizeActionPhase(action string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "precheck":
		return "precheck", true
	case "install":
		return "install", true
	case "upgrade":
		return "upgrade", true
	case "uninstall":
		return "uninstall", true
	case "start":
		return "start", true
	case "stop":
		return "stop", true
	case "restart":
		return "restart", true
	case "reload":
		return "reload", true
	case "configure":
		return "configure", true
	case "configapply":
		return "config_apply", true
	case "verify":
		return "verify", true
	default:
		return "", false
	}
}

func eventTypeForProgress(progress *int) string {
	if progress == nil {
		return "phase"
	}
	return "progress"
}
