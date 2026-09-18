package cluster

import (
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	LifecycleEnterMaintenance = "enter_maintenance"
	LifecycleExitMaintenance  = "exit_maintenance"
	LifecycleDrain            = "drain"
	LifecycleResume           = "resume"
	LifecycleDisable          = "disable"
	LifecycleEnable           = "enable"
	LifecycleMarkDelete       = "mark_delete"
	LifecycleRestoreDelete    = "restore_delete"
)

func (m *Manager) ApplyLifecycle(nodeID uint, action string) (models.ClusterNode, error) {
	var node models.ClusterNode
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&node, nodeID).Error; err != nil {
			return err
		}
		if !node.Enabled && node.LifecycleStatus == models.ClusterNodeLifecycleActive {
			node.LifecycleStatus = models.ClusterNodeLifecycleDisabled
		}
		next, enabled, err := lifecycleTransition(node, action)
		if err != nil {
			return err
		}
		if action == LifecycleMarkDelete {
			var running int64
			if err := tx.Model(&models.ClusterTask{}).Where("node_id = ? AND status = ?", node.ID, models.ClusterTaskStatusRunning).Count(&running).Error; err != nil {
				return err
			}
			if running > 0 {
				return ErrNodeLifecycle
			}
		}
		updates := map[string]any{"lifecycle_status": next, "enabled": enabled}
		if next == models.ClusterNodeLifecycleDisabled || next == models.ClusterNodeLifecyclePendingDelete {
			updates["status"] = models.ClusterNodeStatusOffline
		}
		if next == models.ClusterNodeLifecyclePendingDelete {
			now := time.Now()
			updates["departed_at"] = &now
			token, tokenErr := generateToken()
			if tokenErr != nil {
				return tokenErr
			}
			updates["token_hash"] = hashToken(token)
		}
		return tx.Model(&models.ClusterNode{}).Where("id = ?", node.ID).Updates(updates).Error
	})
	if err != nil {
		return models.ClusterNode{}, err
	}
	if action == LifecycleDrain {
		if err := m.cancelQueuedNodeTasks(node.ID, "node_draining", "节点开始排空，尚未领取的普通任务已取消", true); err != nil {
			return models.ClusterNode{}, err
		}
		_ = m.finishDrainIfIdle(node.ID)
	}
	if action == LifecycleMarkDelete {
		if err := m.cancelQueuedNodeTasks(node.ID, "node_pending_delete", "节点已标记待删除，等待中的任务已取消", false); err != nil {
			return models.ClusterNode{}, err
		}
	}
	return m.GetNode(node.ID)
}

func lifecycleTransition(node models.ClusterNode, action string) (string, bool, error) {
	current := node.LifecycleStatus
	if current == "" {
		current = models.ClusterNodeLifecycleActive
	}
	action = strings.TrimSpace(action)
	switch action {
	case LifecycleEnterMaintenance:
		if current != models.ClusterNodeLifecycleActive {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleMaintenance, true, nil
	case LifecycleExitMaintenance:
		if current != models.ClusterNodeLifecycleMaintenance {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleActive, true, nil
	case LifecycleDrain:
		if current != models.ClusterNodeLifecycleActive && current != models.ClusterNodeLifecycleMaintenance {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleDraining, true, nil
	case LifecycleResume:
		if current != models.ClusterNodeLifecycleDraining && current != models.ClusterNodeLifecycleDrained {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleActive, true, nil
	case LifecycleDisable:
		if current == models.ClusterNodeLifecycleDisabled || current == models.ClusterNodeLifecyclePendingDelete {
			return "", node.Enabled, ErrNodeLifecycle
		}
		if current != models.ClusterNodeLifecycleDrained && node.ConnectionStatus != models.ClusterNodeStatusOffline && node.Status != models.ClusterNodeStatusOffline {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleDisabled, false, nil
	case LifecycleEnable:
		if current != models.ClusterNodeLifecycleDisabled {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleActive, true, nil
	case LifecycleMarkDelete:
		if current != models.ClusterNodeLifecycleDrained && current != models.ClusterNodeLifecycleDisabled {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecyclePendingDelete, false, nil
	case LifecycleRestoreDelete:
		if current != models.ClusterNodeLifecyclePendingDelete {
			return "", node.Enabled, ErrNodeLifecycle
		}
		return models.ClusterNodeLifecycleDisabled, false, nil
	default:
		return "", node.Enabled, ErrNodeLifecycle
	}
}

func (m *Manager) cancelQueuedNodeTasks(nodeID uint, code, message string, ordinaryOnly bool) error {
	var tasks []models.ClusterTask
	query := m.db.Where("node_id = ? AND status = ?", nodeID, models.ClusterTaskStatusQueued)
	if ordinaryOnly {
		query = query.Where("type NOT IN ?", maintenanceTaskTypes())
	}
	if err := query.Find(&tasks).Error; err != nil {
		return err
	}
	now := time.Now()
	for i := range tasks {
		task := &tasks[i]
		if err := m.db.Model(task).Updates(map[string]any{
			"status": models.ClusterTaskStatusCanceled, "stage": models.ClusterTaskStatusCanceled,
			"progress": 100, "finished_at": &now,
		}).Error; err != nil {
			return err
		}
		_ = m.appendTaskEvent(task.ID, models.ClusterTaskStatusCanceled, models.ClusterTaskStatusCanceled, "warning", code, 100, message)
		_ = m.updateBatchStatus(task.BatchID)
	}
	return nil
}

func (m *Manager) finishDrainIfIdle(nodeID uint) error {
	var node models.ClusterNode
	if err := m.db.First(&node, nodeID).Error; err != nil {
		return err
	}
	if node.LifecycleStatus != models.ClusterNodeLifecycleDraining {
		return nil
	}
	var running int64
	if err := m.db.Model(&models.ClusterTask{}).Where("node_id = ? AND status = ?", nodeID, models.ClusterTaskStatusRunning).Count(&running).Error; err != nil {
		return err
	}
	if running == 0 {
		return m.db.Model(&models.ClusterNode{}).Where("id = ? AND lifecycle_status = ?", nodeID, models.ClusterNodeLifecycleDraining).Update("lifecycle_status", models.ClusterNodeLifecycleDrained).Error
	}
	return nil
}

func (m *Manager) validateLifecycle(nodeID uint, action string) error {
	node, err := m.GetNode(nodeID)
	if err != nil {
		return err
	}
	_, _, err = lifecycleTransition(node, action)
	return err
}

func isLifecycleAction(action string) bool {
	for _, value := range []string{LifecycleEnterMaintenance, LifecycleExitMaintenance, LifecycleDrain, LifecycleResume, LifecycleDisable, LifecycleEnable, LifecycleMarkDelete, LifecycleRestoreDelete} {
		if action == value {
			return true
		}
	}
	return false
}
