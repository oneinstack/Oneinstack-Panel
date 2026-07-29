package models

import "time"

const (
	ApprovalStatusPending   = "pending"
	ApprovalStatusApproved  = "approved"
	ApprovalStatusRejected  = "rejected"
	ApprovalStatusExpired   = "expired"
	ApprovalStatusExecuting = "executing"
	ApprovalStatusCompleted = "completed"
	ApprovalStatusFailed    = "failed"
	ApprovalStatusCanceled  = "canceled"
)

type ApprovalRequest struct {
	ID              string     `json:"id" gorm:"primaryKey;size:64"`
	Module          string     `json:"module" gorm:"size:32;index;not null"`
	Action          string     `json:"action" gorm:"size:96;index;not null"`
	ResourceID      string     `json:"resourceId" gorm:"size:128;index"`
	ResourceName    string     `json:"resourceName" gorm:"size:255"`
	RiskLevel       string     `json:"riskLevel" gorm:"size:16;not null"`
	Status          string     `json:"status" gorm:"size:24;index;not null"`
	Reason          string     `json:"reason" gorm:"size:255"`
	PayloadSnapshot string     `json:"payloadSnapshot" gorm:"type:text;not null"`
	ResultPayload   string     `json:"-" gorm:"type:text"`
	RequestedBy     int64      `json:"requestedBy" gorm:"index;not null"`
	RequestedByName string     `json:"requestedByName" gorm:"size:128;not null"`
	ApprovedBy      int64      `json:"approvedBy"`
	ApprovedByName  string     `json:"approvedByName" gorm:"size:128"`
	ApprovedAt      *time.Time `json:"approvedAt"`
	RejectedBy      int64      `json:"rejectedBy"`
	RejectedByName  string     `json:"rejectedByName" gorm:"size:128"`
	RejectedAt      *time.Time `json:"rejectedAt"`
	ReviewComment   string     `json:"reviewComment" gorm:"size:255"`
	ExpiresAt       time.Time  `json:"expiresAt" gorm:"index;not null"`
	BoundTaskType   string     `json:"boundTaskType" gorm:"size:64"`
	BoundTaskID     string     `json:"boundTaskId" gorm:"size:64;index"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}
