package cluster

import (
	"context"
	"log"
	"strconv"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
)

// RunHealthSupervisor evaluates controller-owned checks without depending on
// an open browser page. Node observations remain separately authenticated.
func RunHealthSupervisor(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		var lastLocalReport time.Time
		for {
			if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) == ClusterRoleController {
				manager, err := NewManager(app.DB())
				if err == nil {
					checkCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
					if sweepErr := manager.sweepHealth(checkCtx); sweepErr != nil {
						log.Printf("evaluate cluster health: %v", sweepErr)
					}
					cancel()
					if time.Since(lastLocalReport) >= 5*time.Minute {
						localCtx, localCancel := context.WithTimeout(ctx, 45*time.Second)
						items, collectErr := CollectLocalHealth(localCtx)
						if collectErr == nil {
							seen := make(map[string]struct{}, len(items))
							for _, item := range items {
								seen[healthObservationKey(item)] = struct{}{}
								if applyErr := manager.applyHealthObservation(localCtx, 0, item, false); applyErr != nil {
									collectErr = applyErr
									break
								}
							}
							if collectErr == nil {
								collectErr = manager.reconcileHealthResources(localCtx, 0, seen)
							}
						}
						if collectErr != nil {
							log.Printf("collect local cluster health: %v", collectErr)
						} else {
							lastLocalReport = time.Now()
						}
						localCancel()
					}
				} else {
					log.Printf("initialize cluster health supervisor: %v", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) sweepHealth(ctx context.Context) error {
	now := time.Now().UTC()
	if _, err := m.ExpireStaleNodes(now); err != nil {
		return err
	}
	var nodes []models.ClusterNode
	if err := m.db.Find(&nodes).Error; err != nil {
		return err
	}
	for _, node := range nodes {
		status, reason := healthHealthy, "node_online"
		if !node.Enabled || node.DepartedAt != nil || node.LifecycleStatus == models.ClusterNodeLifecycleDisabled || node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete {
			status, reason = healthDisabled, "node_disabled"
		} else if node.Status == models.ClusterNodeStatusOffline || node.Status == models.ClusterNodeStatusError {
			status, reason = healthCritical, "node_offline"
		} else if node.Status != models.ClusterNodeStatusOnline {
			status, reason = healthUnknown, "node_not_registered"
		}
		item := HealthObservation{ResourceType: "node", ResourceID: strconv.FormatUint(uint64(node.ID), 10),
			Check: "online", Name: node.Name, Status: status, Reason: reason, ObservedAt: now}
		if err := m.applyHealthObservation(ctx, node.ID, item, false); err != nil {
			return err
		}
	}
	var failedTasks []models.ClusterTask
	if err := m.db.Where("status = ? AND finished_at >= ?", models.ClusterTaskStatusFailed, now.Add(-10*time.Minute)).
		Order("finished_at DESC").Limit(100).Find(&failedTasks).Error; err != nil {
		return err
	}
	for _, task := range failedTasks {
		item := HealthObservation{ResourceType: "task", ResourceID: strconv.FormatUint(task.ID, 10),
			Check: "result", Name: task.Type, Status: healthCritical, Reason: "task_failed", ObservedAt: now}
		if err := m.applyHealthObservation(ctx, task.NodeID, item, false); err != nil {
			return err
		}
	}
	if err := m.MarkStaleHealthUnknown(now); err != nil {
		return err
	}
	return m.ProbeWebsiteEntries(ctx)
}
