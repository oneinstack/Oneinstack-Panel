package models

import "time"

const (
	ContainerTaskOperationPull              = "pull"
	ContainerTaskOperationBuild             = "build"
	ContainerTaskOperationCreate            = "create"
	ContainerTaskOperationNetworkConnect    = "network.connect"
	ContainerTaskOperationNetworkDisconnect = "network.disconnect"
	ContainerTaskOperationNetworkReconnect  = "network.reconnect"
	ContainerTaskOperationComposeCreate     = "compose.create"
	ContainerTaskOperationComposeEdit       = "compose.edit"
	ContainerTaskOperationComposeStart      = "compose.start"
	ContainerTaskOperationComposeStop       = "compose.stop"
	ContainerTaskOperationComposeRestart    = "compose.restart"
	ContainerTaskOperationComposeUpdate     = "compose.update"
	ContainerTaskOperationComposeDelete     = "compose.delete"
	ContainerTaskStatusQueued               = "queued"
	ContainerTaskStatusResolving            = "resolving"
	ContainerTaskStatusPulling              = "pulling"
	ContainerTaskStatusBuilding             = "building"
	ContainerTaskStatusCreating             = "creating"
	ContainerTaskStatusVerifying            = "verifying"
	ContainerTaskStatusCanceling            = "canceling"
	ContainerTaskStatusSucceeded            = "succeeded"
	ContainerTaskStatusFailed               = "failed"
	ContainerTaskStatusCanceled             = "canceled"
	ContainerTaskStatusInterrupted          = "interrupted"
)

type ContainerTask struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Operation       string     `json:"operation" gorm:"size:32;not null;default:create;index"`
	Name            string     `json:"name" gorm:"size:128;not null;index:idx_container_task_name_status"`
	Image           string     `json:"image" gorm:"size:256;not null"`
	Network         string     `json:"network,omitempty" gorm:"size:256"`
	Status          string     `json:"status" gorm:"size:32;not null;index:idx_container_task_status_created"`
	Phase           string     `json:"phase" gorm:"size:32;not null;default:queued"`
	PhaseProgress   *int       `json:"phaseProgress,omitempty"`
	Progress        int        `json:"progress" gorm:"not null;default:0"`
	Message         string     `json:"message" gorm:"size:512"`
	ErrorCode       string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage    string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	FailurePhase    string     `json:"failurePhase,omitempty" gorm:"size:32"`
	ContainerID     string     `json:"containerId,omitempty" gorm:"size:128"`
	RequestedBy     int64      `json:"requestedBy" gorm:"not null;index"`
	CancelRequested bool       `json:"cancelRequested" gorm:"not null;default:false"`
	EventSeq        int64      `json:"eventSeq" gorm:"not null;default:0"`
	RequestJSON     string     `json:"-" gorm:"type:text;not null"`
	LogPath         string     `json:"-" gorm:"size:1024"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	HeartbeatAt     *time.Time `json:"heartbeatAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" gorm:"index:idx_container_task_status_created,priority:2"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (ContainerTask) TableName() string { return "container_task" }

func IsContainerTaskTerminal(status string) bool {
	return status == ContainerTaskStatusSucceeded || status == ContainerTaskStatusFailed ||
		status == ContainerTaskStatusCanceled || status == ContainerTaskStatusInterrupted
}

func ActiveContainerTaskStatuses() []string {
	return []string{
		ContainerTaskStatusQueued,
		ContainerTaskStatusResolving,
		ContainerTaskStatusPulling,
		ContainerTaskStatusBuilding,
		ContainerTaskStatusCreating,
		ContainerTaskStatusVerifying,
		ContainerTaskStatusCanceling,
	}
}

type ContainerTaskEvent struct {
	ID            uint64    `json:"-" gorm:"primaryKey;autoIncrement"`
	TaskID        string    `json:"taskId" gorm:"size:36;not null;uniqueIndex:idx_container_task_event_seq,priority:1"`
	Seq           int64     `json:"seq" gorm:"not null;uniqueIndex:idx_container_task_event_seq,priority:2"`
	Type          string    `json:"type" gorm:"size:32;not null"`
	Level         string    `json:"level" gorm:"size:16;not null"`
	Status        string    `json:"status" gorm:"size:32"`
	Phase         string    `json:"phase" gorm:"size:32"`
	PhaseProgress *int      `json:"phaseProgress,omitempty"`
	Progress      int       `json:"progress"`
	Message       string    `json:"message" gorm:"size:512"`
	DetailsJSON   string    `json:"details,omitempty" gorm:"type:text"`
	Log           string    `json:"log,omitempty" gorm:"type:text"`
	Code          string    `json:"code,omitempty" gorm:"size:64"`
	CreatedAt     time.Time `json:"createdAt" gorm:"index:idx_container_task_event_created"`
}

func (ContainerTaskEvent) TableName() string { return "container_task_event" }
