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
	ID              uint64     `gorm:"primaryKey" json:"id"`
	NodeID          uint       `gorm:"index;not null" json:"nodeId"`
	BatchID         string     `gorm:"size:64;index" json:"batchId,omitempty"`
	Type            string     `gorm:"size:120;index;not null" json:"type"`
	IdempotencyKey  string     `gorm:"size:160;uniqueIndex" json:"idempotencyKey,omitempty"`
	Payload         string     `gorm:"type:text;not null" json:"payload"`
	Result          string     `gorm:"type:text" json:"result,omitempty"`
	Error           string     `gorm:"size:1024" json:"error,omitempty"`
	Status          string     `gorm:"size:16;index;not null;default:queued" json:"status"`
	Stage           string     `gorm:"size:64;index" json:"stage,omitempty"`
	Progress        int        `gorm:"not null;default:0" json:"progress"`
	Attempts        int        `gorm:"not null;default:0" json:"attempts"`
	MaxAttempts     int        `gorm:"not null;default:3" json:"maxAttempts"`
	RequestedBy     int64      `gorm:"index" json:"requestedBy,omitempty"`
	CancelRequested bool       `gorm:"index;not null;default:false" json:"cancelRequested"`
	Cancelable      bool       `gorm:"not null;default:true" json:"cancelable"`
	QueuedAt        time.Time  `gorm:"index;not null" json:"queuedAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	LeaseExpiresAt  *time.Time `gorm:"index" json:"leaseExpiresAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type ClusterTaskEvent struct {
	ID         uint64    `gorm:"primaryKey" json:"id"`
	TaskID     uint64    `gorm:"uniqueIndex:idx_cluster_task_event_seq;not null" json:"taskId"`
	Sequence   uint64    `gorm:"uniqueIndex:idx_cluster_task_event_seq;not null" json:"sequence"`
	Stage      string    `gorm:"size:64;index" json:"stage"`
	Status     string    `gorm:"size:16;index" json:"status"`
	Level      string    `gorm:"size:16;index" json:"level"`
	Code       string    `gorm:"size:64;index" json:"code,omitempty"`
	Progress   int       `json:"progress"`
	Message    string    `gorm:"size:1024" json:"message,omitempty"`
	OccurredAt time.Time `gorm:"index;not null" json:"occurredAt"`
}

type ClusterBatchOperation struct {
	ID             string     `gorm:"primaryKey;size:64" json:"id"`
	Action         string     `gorm:"size:64;index;not null" json:"action"`
	Status         string     `gorm:"size:16;index;not null" json:"status"`
	NodeIDs        []uint     `gorm:"serializer:json;type:text" json:"nodeIds"`
	Total          int        `gorm:"not null" json:"total"`
	Queued         int        `gorm:"not null" json:"queued"`
	Running        int        `gorm:"not null" json:"running"`
	Succeeded      int        `gorm:"not null" json:"succeeded"`
	Failed         int        `gorm:"not null" json:"failed"`
	Canceled       int        `gorm:"not null" json:"canceled"`
	MaxConcurrency int        `gorm:"not null;default:1" json:"maxConcurrency"`
	RequestedBy    int64      `gorm:"index" json:"requestedBy,omitempty"`
	Error          string     `gorm:"size:1024" json:"error,omitempty"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	CreatedAt      time.Time  `gorm:"index;not null" json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type ClusterBatchPreview struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	Action      string    `gorm:"size:64;index;not null" json:"action"`
	NodeIDs     []uint    `gorm:"serializer:json;type:text" json:"nodeIds"`
	Fingerprint string    `gorm:"size:64;not null" json:"fingerprint"`
	Payload     string    `gorm:"type:text" json:"-"`
	ExpiresAt   time.Time `gorm:"index;not null" json:"expiresAt"`
	CreatedAt   time.Time `json:"createdAt"`
}
