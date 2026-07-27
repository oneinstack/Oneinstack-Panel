package models

import "time"

const (
	WebsiteTaskOperationBackup  = "backup"
	WebsiteTaskOperationRestore = "restore"
	WebsiteTaskOperationDelete  = "delete"
)

const (
	WebsiteTaskStatusQueued      = "queued"
	WebsiteTaskStatusRunning     = "running"
	WebsiteTaskStatusCanceling   = "canceling"
	WebsiteTaskStatusSucceeded   = "succeeded"
	WebsiteTaskStatusFailed      = "failed"
	WebsiteTaskStatusCanceled    = "canceled"
	WebsiteTaskStatusInterrupted = "interrupted"
)

const (
	WebsiteBackupSourceManual     = "manual"
	WebsiteBackupSourcePreRestore = "pre_restore"
	WebsiteBackupSourcePreDelete  = "pre_delete"
)

// WebsiteTask stores the durable state of website backup, restore, and safe
// deletion. Server paths never leave the backend.
type WebsiteTask struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Operation       string     `json:"operation" gorm:"size:16;not null"`
	WebsiteID       int64      `json:"websiteId" gorm:"not null;index:idx_website_task_site_created"`
	WebsiteName     string     `json:"websiteName" gorm:"size:253;not null"`
	DatabaseID      int64      `json:"databaseId,omitempty" gorm:"index"`
	DatabaseName    string     `json:"databaseName,omitempty" gorm:"size:64"`
	SourceBackupID  string     `json:"sourceBackupId,omitempty" gorm:"size:36;index"`
	ResultBackupID  string     `json:"resultBackupId,omitempty" gorm:"size:36;index"`
	SafetyBackupID  string     `json:"safetyBackupId,omitempty" gorm:"size:36;index"`
	DeleteFiles     bool       `json:"deleteFiles" gorm:"not null;default:false"`
	Status          string     `json:"status" gorm:"size:32;not null;index:idx_website_task_status_created"`
	Progress        int        `json:"progress" gorm:"not null;default:0"`
	Message         string     `json:"message" gorm:"size:512"`
	ErrorCode       string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage    string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null;index:idx_website_task_user_created"`
	CancelRequested bool       `json:"cancelRequested" gorm:"not null;default:false"`
	LogPath         string     `json:"-" gorm:"size:1024"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeatAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_website_task_site_created,priority:2;index:idx_website_task_status_created,priority:2;index:idx_website_task_user_created,priority:2"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (WebsiteTask) TableName() string {
	return "website_task"
}

// WebsiteBackup is the verified archive metadata. FilePath is deliberately
// excluded from JSON and is re-derived from the configured backup root.
type WebsiteBackup struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	WebsiteID    int64     `json:"websiteId" gorm:"not null;index:idx_website_backup_site_created"`
	WebsiteName  string    `json:"websiteName" gorm:"size:253;not null"`
	DatabaseID   int64     `json:"databaseId,omitempty" gorm:"index"`
	DatabaseName string    `json:"databaseName,omitempty" gorm:"size:64"`
	Source       string    `json:"source" gorm:"size:24;not null"`
	FileName     string    `json:"fileName" gorm:"size:255;not null"`
	FilePath     string    `json:"-" gorm:"size:1024;not null"`
	SizeBytes    int64     `json:"sizeBytes" gorm:"not null"`
	SHA256       string    `json:"sha256" gorm:"size:64;not null"`
	CreatedBy    int64     `json:"createdBy" gorm:"not null"`
	CreatedAt    time.Time `json:"createdAt" gorm:"index:idx_website_backup_site_created,priority:2"`
}

func (WebsiteBackup) TableName() string {
	return "website_backup"
}

type WebsiteOperationLock struct {
	WebsiteID   int64     `json:"-" gorm:"primaryKey"`
	TaskID      string    `json:"-" gorm:"size:36;not null;uniqueIndex"`
	AcquiredAt  time.Time `json:"-"`
	HeartbeatAt time.Time `json:"-"`
}

func (WebsiteOperationLock) TableName() string {
	return "website_operation_lock"
}

func IsWebsiteTaskTerminal(status string) bool {
	switch status {
	case WebsiteTaskStatusSucceeded,
		WebsiteTaskStatusFailed,
		WebsiteTaskStatusCanceled,
		WebsiteTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func ActiveWebsiteTaskStatuses() []string {
	return []string{
		WebsiteTaskStatusQueued,
		WebsiteTaskStatusRunning,
		WebsiteTaskStatusCanceling,
	}
}
