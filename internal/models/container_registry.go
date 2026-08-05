package models

import "time"

const (
	RegistryStatusUnknown = "unknown"
	RegistryStatusSuccess = "success"
	RegistryStatusFailed  = "failed"
)

// ContainerRegistry stores registry metadata. PasswordEnc is never serialized
// and is encrypted with the instance credential key.
type ContainerRegistry struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:120;not null;uniqueIndex" json:"name"`
	Address       string     `gorm:"size:255;not null" json:"address"`
	Protocol      string     `gorm:"size:8;not null;default:https" json:"protocol"`
	AuthEnabled   bool       `gorm:"not null;default:false" json:"authEnabled"`
	Username      string     `gorm:"size:128" json:"username,omitempty"`
	PasswordEnc   string     `gorm:"type:text" json:"-"`
	Status        string     `gorm:"size:16;index;not null;default:unknown" json:"status"`
	StatusMessage string     `gorm:"size:512" json:"statusMessage,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}
