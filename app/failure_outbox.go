package app

import (
	"fmt"

	"gorm.io/gorm"
)

// SQLite triggers insert the outbox entry in the same transaction that makes a
// user task terminal. No historical rows are scanned when this is installed.
func initFailureOutbox(db *gorm.DB) error {
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS failure_outbox (
		id TEXT PRIMARY KEY NOT NULL,
		task_type TEXT NOT NULL,
		task_id TEXT NOT NULL,
		created_at TEXT NOT NULL,
		reported_at TEXT,
		attempts INTEGER NOT NULL DEFAULT 0,
		next_attempt_at TEXT,
		last_error TEXT NOT NULL DEFAULT ''
	)`).Error; err != nil {
		return err
	}
	if err := db.Exec(`CREATE INDEX IF NOT EXISTS failure_outbox_pending_idx ON failure_outbox(reported_at, next_attempt_at, created_at)`).Error; err != nil {
		return err
	}
	for _, source := range []struct{ table, kind, id, eligible string }{
		{"software_task", "software", "NEW.id", "NEW.requested_by > 0"},
		{"container_task", "container", "NEW.id", "NEW.requested_by > 0"},
		{"cluster_tasks", "cluster", "NEW.id", "NEW.requested_by > 0 AND NEW.type <> 'service.action.preflight.v1'"},
		{"certificate_task", "certificate", "NEW.id", "NEW.requested_by > 0"},
		{"database_task", "database", "NEW.id", "NEW.requested_by > 0"},
		{"website_task", "website", "NEW.id", "NEW.requested_by > 0"},
		{"file_archive_task", "file_archive", "NEW.id", "NEW.requested_by > 0"},
		{"fail2ban_task", "fail2ban", "NEW.id", "NEW.requested_by > 0 AND NEW.triggered_by <> 'system'"},
		{"job_execution", "cron", "NEW.id", "NEW.trigger = 'manual'"},
		{"artifact_cleanup_task", "artifact_cleanup", "NEW.id", "NEW.requested_by > 0 AND NEW.automatic = 0"},
	} {
		for _, action := range []string{"INSERT", "UPDATE OF status"} {
			name := fmt.Sprintf("failure_outbox_%s_%c", source.kind, action[0])
			condition := "NEW.status IN ('failed','timeout') AND " + source.eligible
			if action != "INSERT" {
				condition += " AND OLD.status <> NEW.status"
			}
			statement := fmt.Sprintf(`CREATE TRIGGER IF NOT EXISTS %s AFTER %s ON %s
				WHEN %s BEGIN
				INSERT OR IGNORE INTO failure_outbox(id, task_type, task_id, created_at)
				VALUES ('%s:' || %s, '%s', CAST(%s AS TEXT), strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ','now'));
				END`, name, action, source.table, condition, source.kind, source.id, source.kind, source.id)
			if err := db.Exec(statement).Error; err != nil {
				return fmt.Errorf("install failure outbox trigger %s: %w", name, err)
			}
		}
	}
	return nil
}
