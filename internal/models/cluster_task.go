package models

import "time"

const (
	ClusterTaskStatusQueued    = "queued"
	ClusterTaskStatusRunning   = "running"
	ClusterTaskStatusSucceeded = "succeeded"
	ClusterTaskStatusFailed    = "failed"
	ClusterTaskStatusCanceled  = "canceled"
)

type ClusterTask struct {
	ID             uint64     `gorm:"primaryKey" json:"id"`
	NodeID         uint       `gorm:"index;not null" json:"nodeId"`
	Type           string     `gorm:"size:120;index;not null" json:"type"`
	IdempotencyKey string     `gorm:"size:160;uniqueIndex" json:"idempotencyKey,omitempty"`
	Payload        string     `gorm:"type:text;not null" json:"payload"`
	Result         string     `gorm:"type:text" json:"result,omitempty"`
	Error          string     `gorm:"size:1024" json:"error,omitempty"`
	Status         string     `gorm:"size:16;index;not null;default:queued" json:"status"`
	Stage          string     `gorm:"size:64;index" json:"stage,omitempty"`
	Progress       int        `gorm:"not null;default:0" json:"progress"`
	Attempts       int        `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts    int        `gorm:"not null;default:3" json:"maxAttempts"`
	QueuedAt       time.Time  `gorm:"index;not null" json:"queuedAt"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	LeaseExpiresAt *time.Time `gorm:"index" json:"leaseExpiresAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}
