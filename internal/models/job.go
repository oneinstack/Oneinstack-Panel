package models

import "time"

type JobExecution struct {
	ID              uint      `gorm:"primaryKey" json:"id"`
	CronJobID       uint      `gorm:"index" json:"cron_job_id"`
	StartTime       time.Time `gorm:"not null" json:"start_time"`
	EndTime         time.Time `gorm:"not null" json:"end_time"`
	Status          string    `gorm:"type:varchar(20)" json:"status"` // running/success/failed
	Trigger         string    `gorm:"type:varchar(16);not null;default:scheduled" json:"trigger"`
	Output          string    `gorm:"type:text" json:"output"`
	OutputTruncated bool      `gorm:"not null;default:false" json:"output_truncated"`
	ErrorCode       string    `gorm:"type:varchar(64)" json:"error_code,omitempty"`
	ExitCode        int       `gorm:"not null" json:"exit_code"`
	DurationMs      int64     `gorm:"not null;default:0" json:"duration_ms"`
}

func (j *JobExecution) TableName() string {
	return "job_execution"
}
