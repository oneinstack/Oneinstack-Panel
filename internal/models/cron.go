package models

import (
	"time"

	"gorm.io/gorm"
)

type Cron struct {
	ID           int            `gorm:"primaryKey;autoIncrement" json:"id"`
	CronType     string         `gorm:"type:varchar(255);not null;index" json:"cron_type"`
	Name         string         `gorm:"type:varchar(255);not null" json:"name"`
	CronTimes    string         `gorm:"type:varchar(255);not null;index" json:"cron_times"`
	ShellContent string         `gorm:"type:text;" json:"shell_content"`
	Status       int            `gorm:"type:tinyint;not null;default:0;index" json:"status"`
	CreatedAt    time.Time      `gorm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time      `gorm:"autoUpdateTime" json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"deleted_at"`
}

func (c *Cron) TableName() string {
	return "cron"
}

func (c *Cron) BeforeCreate(tx *gorm.DB) (err error) {
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	return
}
