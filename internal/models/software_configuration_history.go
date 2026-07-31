package models

import "time"

const (
	SoftwareConfigurationStatusPending     = "pending"
	SoftwareConfigurationStatusSucceeded   = "succeeded"
	SoftwareConfigurationStatusFailed      = "failed"
	SoftwareConfigurationStatusCanceled    = "canceled"
	SoftwareConfigurationStatusInterrupted = "interrupted"
)

// SoftwareConfigurationHistory records the safe, managed configuration values
// before and after a component configuration task. Only whitelisted component
// parameters are stored; website paths, credentials, and free-form files never
// enter this table.
type SoftwareConfigurationHistory struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	TaskID          string     `json:"taskId" gorm:"size:36;not null;uniqueIndex"`
	Component       string     `json:"component" gorm:"size:64;not null;index:idx_software_config_component_created"`
	SoftwareKey     string     `json:"softwareKey" gorm:"size:64;not null"`
	SoftwareVersion string     `json:"softwareVersion" gorm:"size:64;not null"`
	BaseRevision    string     `json:"baseRevision" gorm:"size:64;not null"`
	BeforeJSON      string     `json:"-" gorm:"type:text;not null"`
	AfterJSON       string     `json:"-" gorm:"type:text;not null"`
	Status          string     `json:"status" gorm:"size:32;not null;index"`
	RestoreFromID   string     `json:"restoreFromId,omitempty" gorm:"size:36;index"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null;index"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_software_config_component_created,priority:2"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (SoftwareConfigurationHistory) TableName() string {
	return "software_configuration_history"
}
