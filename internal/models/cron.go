package models

import (
	"time"

	"gorm.io/gorm"
)

type CronJob struct {
	ID          uint      `gorm:"primaryKey" json:"id"`
	Name        string    `gorm:"type:varchar(255)" json:"name"`
	Command     string    `gorm:"type:text;" json:"command"`
	Schedule    string    `gorm:"type:text;" json:"schedule"`
	Description string    `gorm:"type:varchar(255)" json:"description"`
	Enabled     bool      `gorm:"default:true" json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (c *CronJob) TableName() string {
	return "cron"
}

func (c *CronJob) BeforeCreate(tx *gorm.DB) (err error) {
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()
	return
}
