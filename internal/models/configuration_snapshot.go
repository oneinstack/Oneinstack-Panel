package models

import "time"

const (
	ConfigurationSnapshotResourceWebsite     = "website"
	ConfigurationSnapshotResourceNginx       = "nginx"
	ConfigurationSnapshotResourceFirewall    = "firewall"
	ConfigurationSnapshotResourcePanelAccess = "panel_access"

	ConfigurationSnapshotOperationCreate  = "create"
	ConfigurationSnapshotOperationUpdate  = "update"
	ConfigurationSnapshotOperationDelete  = "delete"
	ConfigurationSnapshotOperationRestore = "restore"

	ConfigurationSnapshotStatusPending        = "pending"
	ConfigurationSnapshotStatusApplying       = "applying"
	ConfigurationSnapshotStatusSucceeded      = "succeeded"
	ConfigurationSnapshotStatusFailed         = "failed"
	ConfigurationSnapshotStatusRolledBack     = "rolled_back"
	ConfigurationSnapshotStatusRollbackFailed = "rollback_failed"
)

// ConfigurationSnapshot stores a redacted, structured before/after view of a
// managed configuration change. Raw artifacts are kept outside the database.
type ConfigurationSnapshot struct {
	ID             string     `json:"id" gorm:"primaryKey;size:36"`
	ResourceType   string     `json:"resourceType" gorm:"size:32;not null;index:idx_config_snapshot_resource_created"`
	ResourceID     string     `json:"resourceId" gorm:"size:128;not null;index:idx_config_snapshot_resource_created"`
	Name           string     `json:"name,omitempty" gorm:"size:255"`
	Version        string     `json:"version,omitempty" gorm:"size:128"`
	BackupAccount  string     `json:"backupAccount,omitempty" gorm:"size:128"`
	SizeBytes      int64      `json:"sizeBytes,omitempty"`
	Description    string     `json:"description,omitempty" gorm:"size:255"`
	Operation      string     `json:"operation" gorm:"size:16;not null"`
	Status         string     `json:"status" gorm:"size:32;not null;index"`
	BeforeRevision string     `json:"beforeRevision,omitempty" gorm:"size:128"`
	AfterRevision  string     `json:"afterRevision,omitempty" gorm:"size:128"`
	BeforeJSON     string     `json:"-" gorm:"type:text;not null"`
	AfterJSON      string     `json:"-" gorm:"type:text;not null"`
	DiffJSON       string     `json:"-" gorm:"type:text;not null"`
	ArtifactPath   string     `json:"-" gorm:"size:1024"`
	ArtifactSHA256 string     `json:"artifactSha256,omitempty" gorm:"size:64"`
	TaskID         string     `json:"taskId,omitempty" gorm:"size:36;index"`
	RequestedBy    int64      `json:"requestedBy" gorm:"not null;index"`
	FailureMessage string     `json:"failureMessage,omitempty" gorm:"size:1024"`
	CreatedAt      time.Time  `json:"createdAt" gorm:"index:idx_config_snapshot_resource_created,priority:3"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

func (ConfigurationSnapshot) TableName() string { return "configuration_snapshot" }
