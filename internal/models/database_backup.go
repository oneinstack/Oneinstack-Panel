package models

import "time"

const (
	DatabaseTaskStatusQueued      = "queued"
	DatabaseTaskStatusRunning     = "running"
	DatabaseTaskStatusCanceling   = "canceling"
	DatabaseTaskStatusSucceeded   = "succeeded"
	DatabaseTaskStatusFailed      = "failed"
	DatabaseTaskStatusCanceled    = "canceled"
	DatabaseTaskStatusInterrupted = "interrupted"
)

const (
	DatabaseBackupSourceManual     = "manual"
	DatabaseBackupSourcePreRestore = "pre_restore"
)

// DatabaseTask stores the durable state of a MySQL backup or restore
// operation. Filesystem paths are deliberately excluded from API responses.
type DatabaseTask struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Operation       string     `json:"operation" gorm:"size:16;not null"`
	LibraryID       int64      `json:"libraryId" gorm:"not null;index:idx_database_task_library_created"`
	DatabaseName    string     `json:"databaseName" gorm:"size:64;not null"`
	SourceBackupID  string     `json:"sourceBackupId,omitempty" gorm:"size:36;index"`
	ResultBackupID  string     `json:"resultBackupId,omitempty" gorm:"size:36;index"`
	SafetyBackupID  string     `json:"safetyBackupId,omitempty" gorm:"size:36;index"`
	Status          string     `json:"status" gorm:"size:32;not null;index:idx_database_task_status_created"`
	Progress        int        `json:"progress" gorm:"not null;default:0"`
	Message         string     `json:"message" gorm:"size:512"`
	ErrorCode       string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage    string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null;index:idx_database_task_user_created"`
	CancelRequested bool       `json:"cancelRequested" gorm:"not null;default:false"`
	LogPath         string     `json:"-" gorm:"size:1024"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeatAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_database_task_library_created,priority:2;index:idx_database_task_status_created,priority:2;index:idx_database_task_user_created,priority:2"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (DatabaseTask) TableName() string {
	return "database_task"
}

// DatabaseBackup is the verified artifact metadata. FilePath never leaves the
// backend; downloads resolve it again against the configured backup root.
type DatabaseBackup struct {
	ID           string    `json:"id" gorm:"primaryKey;size:36"`
	LibraryID    int64     `json:"libraryId" gorm:"not null;index:idx_database_backup_library_created"`
	ConnectionID int64     `json:"connectionId" gorm:"not null"`
	DatabaseName string    `json:"databaseName" gorm:"size:64;not null"`
	Source       string    `json:"source" gorm:"size:24;not null"`
	FileName     string    `json:"fileName" gorm:"size:255;not null"`
	FilePath     string    `json:"-" gorm:"size:1024;not null"`
	SizeBytes    int64     `json:"sizeBytes" gorm:"not null"`
	SHA256       string    `json:"sha256" gorm:"size:64;not null"`
	CreatedBy    int64     `json:"createdBy" gorm:"not null"`
	CreatedAt    time.Time `json:"createdAt" gorm:"index:idx_database_backup_library_created,priority:2"`
}

func (DatabaseBackup) TableName() string {
	return "database_backup"
}

type DatabaseOperationLock struct {
	LibraryID   int64     `json:"-" gorm:"primaryKey"`
	TaskID      string    `json:"-" gorm:"size:36;not null;uniqueIndex"`
	AcquiredAt  time.Time `json:"-"`
	HeartbeatAt time.Time `json:"-"`
}

func (DatabaseOperationLock) TableName() string {
	return "database_operation_lock"
}

func IsDatabaseTaskTerminal(status string) bool {
	switch status {
	case DatabaseTaskStatusSucceeded,
		DatabaseTaskStatusFailed,
		DatabaseTaskStatusCanceled,
		DatabaseTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func ActiveDatabaseTaskStatuses() []string {
	return []string{
		DatabaseTaskStatusQueued,
		DatabaseTaskStatusRunning,
		DatabaseTaskStatusCanceling,
	}
}
