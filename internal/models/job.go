package models

import "time"

type JobExecution struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	CronJobID uint      `gorm:"index" json:"cron_job_id"`
	StartTime time.Time `gorm:"not null" json:"start_time"`
	EndTime   time.Time `gorm:"not null" json:"end_time"`
	Status    string    `gorm:"type:varchar(20)" json:"status"` // running/success/failed
	Output    string    `gorm:"type:text" json:"output"`
	ExitCode  int       `gorm:"not null" json:"exit_code"`
}

func (j *JobExecution) TableName() string {
	return "job_execution"
}
