package models

import "time"

const (
	FileArchiveTaskOperationArchive = "archive"
	FileArchiveTaskOperationExtract = "extract"

	FileArchiveTaskStatusQueued    = "queued"
	FileArchiveTaskStatusRunning   = "running"
	FileArchiveTaskStatusSucceeded = "succeeded"
	FileArchiveTaskStatusFailed    = "failed"
)

// FileArchiveTask is a durable record for an archive or extraction operation. Source and
// target paths are virtual paths rooted at the configured file-management root.
type FileArchiveTask struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Operation       string     `json:"operation" gorm:"size:16;not null;default:archive;index:idx_file_archive_task_operation_status_created,priority:1"`
	SourcePath      string     `json:"sourcePath" gorm:"size:1024;not null"`
	TargetDir       string     `json:"targetDir" gorm:"size:1024;not null"`
	ArchiveName     string     `json:"archiveName,omitempty" gorm:"size:255;not null"`
	ArchiveFormat   string     `json:"archiveFormat,omitempty" gorm:"size:32;not null;default:''"`
	Overwrite       bool       `json:"overwrite,omitempty" gorm:"not null;default:false"`
	FileRootPath    string     `json:"-" gorm:"size:1024;not null;default:''"`
	QuotaBytes      int64      `json:"-" gorm:"not null;default:0"`
	MinFreeBytes    int64      `json:"-" gorm:"not null;default:0"`
	MaxExtractBytes int64      `json:"-" gorm:"not null;default:0"`
	MaxExtractFiles int        `json:"-" gorm:"not null;default:0"`
	ResultPath      string     `json:"resultPath,omitempty" gorm:"size:1024"`
	Entries         int        `json:"entries,omitempty"`
	Bytes           int64      `json:"bytes,omitempty"`
	TotalBytes      int64      `json:"totalBytes"`
	ProcessedBytes  int64      `json:"processedBytes"`
	Progress        int        `json:"progress" gorm:"not null;default:0"`
	CurrentPath     string     `json:"currentPath,omitempty" gorm:"size:1024"`
	Status          string     `json:"status" gorm:"size:16;not null;index:idx_file_archive_task_status_created;index:idx_file_archive_task_operation_status_created,priority:2"`
	Message         string     `json:"message" gorm:"size:512;not null"`
	ErrorCode       string     `json:"errorCode,omitempty" gorm:"size:64"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null;index"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_file_archive_task_status_created,priority:2;index:idx_file_archive_task_operation_status_created,priority:3"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (FileArchiveTask) TableName() string { return "file_archive_task" }
