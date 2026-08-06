package models

import "time"

const (
	ContainerTaskStatusQueued    = "queued"
	ContainerTaskStatusPulling   = "pulling"
	ContainerTaskStatusCreating  = "creating"
	ContainerTaskStatusSucceeded = "succeeded"
	ContainerTaskStatusFailed    = "failed"
)

type ContainerTask struct {
	ID           string     `json:"id" gorm:"primaryKey;size:36"`
	Name         string     `json:"name" gorm:"size:128;not null;index:idx_container_task_name_status"`
	Image        string     `json:"image" gorm:"size:256;not null"`
	Status       string     `json:"status" gorm:"size:32;not null;index:idx_container_task_status_created"`
	Message      string     `json:"message" gorm:"size:512"`
	ErrorMessage string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	ContainerID  string     `json:"containerId,omitempty" gorm:"size:128"`
	RequestedBy  int64      `json:"requestedBy" gorm:"not null;index"`
	RequestJSON  string     `json:"-" gorm:"type:text;not null"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	CreatedAt    time.Time  `json:"createdAt" gorm:"index:idx_container_task_status_created,priority:2"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

func (ContainerTask) TableName() string { return "container_task" }

func IsContainerTaskTerminal(status string) bool {
	return status == ContainerTaskStatusSucceeded || status == ContainerTaskStatusFailed
}

func ActiveContainerTaskStatuses() []string {
	return []string{
		ContainerTaskStatusQueued,
		ContainerTaskStatusPulling,
		ContainerTaskStatusCreating,
	}
}
