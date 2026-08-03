package models

import "time"

// OperationPreview stores a short-lived, single-use authorization for a
// structured system operation. The payload is encrypted and never exposed by
// the management API.
type OperationPreview struct {
	ID               string     `json:"id" gorm:"primaryKey;size:36"`
	Operation        string     `json:"operation" gorm:"size:96;not null;index"`
	UserID           int64      `json:"userId" gorm:"not null;index"`
	RequestHash      string     `json:"requestHash" gorm:"size:64;not null"`
	ResourceVersion  string     `json:"resourceVersion,omitempty" gorm:"size:128"`
	EncryptedPayload string     `json:"-" gorm:"column:encrypted_payload;type:text;not null"`
	PreviewJSON      string     `json:"-" gorm:"type:text;not null"`
	ExpiresAt        time.Time  `json:"expiresAt" gorm:"not null;index"`
	ConsumedAt       *time.Time `json:"consumedAt,omitempty" gorm:"index"`
	CreatedAt        time.Time  `json:"createdAt" gorm:"not null;index"`
}

func (OperationPreview) TableName() string {
	return "operation_preview"
}
