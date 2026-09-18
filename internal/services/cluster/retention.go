package cluster

import (
	"context"
	"fmt"
	"log"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"

	"gorm.io/gorm"
)

const (
	clusterTaskRetentionDays  = 90
	clusterEventRetentionDays = 30
)

type RetentionCleanupResult struct {
	EventsDeleted           int64
	DiagnosisResultsCleared int64
	TasksDeleted            int64
	BatchesDeleted          int64
	PreviewsDeleted         int64
}

func (m *Manager) CleanupRetention(now time.Time) (RetentionCleanupResult, error) {
	result := RetentionCleanupResult{}
	eventCutoff := now.UTC().AddDate(0, 0, -clusterEventRetentionDays)
	taskCutoff := now.UTC().AddDate(0, 0, -clusterTaskRetentionDays)
	terminal := []string{
		models.ClusterTaskStatusSucceeded,
		models.ClusterTaskStatusFailed,
		models.ClusterTaskStatusCanceled,
	}
	err := m.db.Transaction(func(tx *gorm.DB) error {
		deleted := tx.Where("occurred_at < ?", eventCutoff).Delete(&models.ClusterTaskEvent{})
		if deleted.Error != nil {
			return fmt.Errorf("delete expired cluster task events: %w", deleted.Error)
		}
		result.EventsDeleted = deleted.RowsAffected

		cleared := tx.Model(&models.ClusterTask{}).
			Where("type = ? AND status IN ? AND result <> '' AND COALESCE(finished_at, updated_at) < ?", TaskNodeDiagnose, terminal, eventCutoff).
			Update("result", "")
		if cleared.Error != nil {
			return fmt.Errorf("clear expired cluster diagnosis results: %w", cleared.Error)
		}
		result.DiagnosisResultsCleared = cleared.RowsAffected

		expiredTasks := tx.Model(&models.ClusterTask{}).
			Select("id").
			Where("status IN ? AND COALESCE(finished_at, updated_at) < ?", terminal, taskCutoff)
		if err := tx.Where("task_id IN (?)", expiredTasks).Delete(&models.ClusterTaskEvent{}).Error; err != nil {
			return fmt.Errorf("delete events for expired cluster tasks: %w", err)
		}
		deleted = tx.Where("status IN ? AND COALESCE(finished_at, updated_at) < ?", terminal, taskCutoff).Delete(&models.ClusterTask{})
		if deleted.Error != nil {
			return fmt.Errorf("delete expired cluster tasks: %w", deleted.Error)
		}
		result.TasksDeleted = deleted.RowsAffected

		deleted = tx.Where("status IN ? AND COALESCE(finished_at, updated_at) < ?", terminal, taskCutoff).Delete(&models.ClusterBatchOperation{})
		if deleted.Error != nil {
			return fmt.Errorf("delete expired cluster batches: %w", deleted.Error)
		}
		result.BatchesDeleted = deleted.RowsAffected

		deleted = tx.Where("expires_at < ?", now.UTC()).Delete(&models.ClusterBatchPreview{})
		if deleted.Error != nil {
			return fmt.Errorf("delete expired cluster batch previews: %w", deleted.Error)
		}
		result.PreviewsDeleted = deleted.RowsAffected
		return nil
	})
	return result, err
}

func RunRetentionSupervisor(ctx context.Context) {
	go func() {
		cleanup := func() {
			if EffectiveClusterRole(app.ONE_CONFIG.ClusterAgent) != ClusterRoleController {
				return
			}
			manager, err := NewManager(app.DB())
			if err == nil {
				var result RetentionCleanupResult
				result, err = manager.CleanupRetention(time.Now())
				if err == nil && result != (RetentionCleanupResult{}) {
					log.Printf("cluster retention cleanup removed %d events, %d diagnosis results, %d tasks, %d batches and %d previews", result.EventsDeleted, result.DiagnosisResultsCleared, result.TasksDeleted, result.BatchesDeleted, result.PreviewsDeleted)
				}
			}
			if err != nil {
				log.Printf("cluster retention cleanup failed: %v", err)
			}
		}
		cleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
}
