package models

import "time"

const (
	FileArchiveTaskStatusQueued    = "queued"
	FileArchiveTaskStatusRunning   = "running"
	FileArchiveTaskStatusSucceeded = "succeeded"
	FileArchiveTaskStatusFailed    = "failed"
)

// FileArchiveTask is a durable record for an archive operation. Source and
// target paths are virtual paths rooted at the configured file-management root.
type FileArchiveTask struct {
	ID             string     `json:"id" gorm:"primaryKey;size:36"`
	SourcePath     string     `json:"sourcePath" gorm:"size:1024;not null"`
	TargetDir      string     `json:"targetDir" gorm:"size:1024;not null"`
	ArchiveName    string     `json:"archiveName" gorm:"size:255;not null"`
	ResultPath     string     `json:"resultPath,omitempty" gorm:"size:1024"`
	Entries        int        `json:"entries,omitempty"`
	Bytes          int64      `json:"bytes,omitempty"`
	TotalBytes     int64      `json:"totalBytes"`
	ProcessedBytes int64      `json:"processedBytes"`
	Progress       int        `json:"progress" gorm:"not null;default:0"`
	CurrentPath    string     `json:"currentPath,omitempty" gorm:"size:1024"`
	Status         string     `json:"status" gorm:"size:16;not null;index:idx_file_archive_task_status_created"`
	Message        string     `json:"message" gorm:"size:512;not null"`
	RequestedBy    int64      `json:"requestedBy" gorm:"not null;index"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt" gorm:"index:idx_file_archive_task_status_created,priority:2"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func (FileArchiveTask) TableName() string { return "file_archive_task" }
