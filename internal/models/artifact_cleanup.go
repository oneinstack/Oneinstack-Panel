package models

import "time"

// ArtifactCleanupTask records irreversible Panel generated artifact removal.
// The path is kept for execution and audit, but is not returned to ordinary API callers.
type ArtifactCleanupTask struct {
	ID          string     `json:"id" gorm:"primaryKey;size:36"`
	CandidateID string     `json:"candidateId" gorm:"size:64;not null"`
	Kind        string     `json:"kind" gorm:"size:32;not null"`
	Component   string     `json:"component,omitempty" gorm:"size:64"`
	Path        string     `json:"-" gorm:"size:1024;not null"`
	Status      string     `json:"status" gorm:"size:24;not null;index"`
	Bytes       int64      `json:"bytes"`
	RequestedBy int64      `json:"requestedBy"`
	Automatic   bool       `json:"automatic"`
	ErrorCode   string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorText   string     `json:"errorMessage,omitempty" gorm:"size:512"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
}

func (ArtifactCleanupTask) TableName() string { return "artifact_cleanup_task" }
