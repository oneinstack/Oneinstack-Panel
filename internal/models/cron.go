package models

import (
	"time"

	"gorm.io/gorm"
)

type CronJob struct {
	ID                uint              `gorm:"primaryKey" json:"id"`
	Name              string            `gorm:"type:varchar(255)" json:"name"`
	Command           string            `gorm:"type:text;" json:"command"`
	TaskType          string            `gorm:"type:varchar(16);not null;default:shell;index" json:"task_type"`
	TemplateID        string            `gorm:"type:varchar(64);index" json:"template_id,omitempty"`
	TemplateParams    map[string]string `gorm:"serializer:json;type:text" json:"template_params,omitempty"`
	Schedule          string            `gorm:"type:text;" json:"schedule"`
	Description       string            `gorm:"type:varchar(255)" json:"description"`
	Enabled           bool              `gorm:"default:true" json:"enabled"`
	NotifyOnFailure   bool              `gorm:"not null;default:false" json:"notify_on_failure"`
	TimeoutSeconds    int               `gorm:"not null;default:1800" json:"timeout_seconds"`
	ConcurrencyPolicy string            `gorm:"type:varchar(16);not null;default:forbid" json:"concurrency_policy"`
	LastRunAt         *time.Time        `json:"last_run_at,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

func (c *CronJob) TableName() string {
	return "cron"
}

func (c *CronJob) BeforeCreate(tx *gorm.DB) (err error) {
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	return
}
