package models

import "time"

const (
	SoftwareTaskStatusQueued       = "queued"
	SoftwareTaskStatusResolving    = "resolving"
	SoftwareTaskStatusPrechecking  = "prechecking"
	SoftwareTaskStatusInstalling   = "installing"
	SoftwareTaskStatusUpgrading    = "upgrading"
	SoftwareTaskStatusUninstalling = "uninstalling"
	SoftwareTaskStatusStarting     = "starting"
	SoftwareTaskStatusStopping     = "stopping"
	SoftwareTaskStatusRestarting   = "restarting"
	SoftwareTaskStatusReloading    = "reloading"
	SoftwareTaskStatusConfiguring  = "configuring"
	SoftwareTaskStatusVerifying    = "verifying"
	SoftwareTaskStatusFinalizing   = "finalizing"
	SoftwareTaskStatusCanceling    = "canceling"
	SoftwareTaskStatusRollback     = "rolling_back"
	SoftwareTaskStatusSucceeded    = "succeeded"
	SoftwareTaskStatusFailed       = "failed"
	SoftwareTaskStatusCanceled     = "canceled"
	SoftwareTaskStatusInterrupted  = "interrupted"
)

const (
	SoftwareTaskRollbackNotRequired = "not_required"
	SoftwareTaskRollbackRunning     = "running"
	SoftwareTaskRollbackSucceeded   = "succeeded"
	SoftwareTaskRollbackFailed      = "failed"
)

// SoftwareTask is the durable snapshot of an installation, upgrade, or
// uninstall operation. Secret parameters and the internal log path are never
// serialized by the management API.
type SoftwareTask struct {
	ID               string     `json:"id" gorm:"primaryKey;size:36"`
	Operation        string     `json:"operation" gorm:"size:16;not null"`
	Component        string     `json:"component" gorm:"size:64;not null;index:idx_software_task_component_created"`
	SwitchRequested  bool       `json:"switchRequested,omitempty" gorm:"not null;default:false"`
	SoftwareKey      string     `json:"softwareKey" gorm:"size:64;not null"`
	RequestedVersion string     `json:"requestedVersion" gorm:"size:64;not null"`
	ResolvedVersion  string     `json:"resolvedVersion,omitempty" gorm:"size:64"`
	PackageSource    string     `json:"packageSource,omitempty" gorm:"size:32"`
	Status           string     `json:"status" gorm:"size:32;not null;index:idx_software_task_status_created"`
	Phase            string     `json:"phase" gorm:"size:32;not null"`
	PhaseProgress    *int       `json:"phaseProgress,omitempty"`
	Progress         int        `json:"progress" gorm:"not null;default:0"`
	Message          string     `json:"message" gorm:"size:512"`
	ErrorCode        string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage     string     `json:"errorMessage,omitempty" gorm:"size:1024"`
	FailurePhase     string     `json:"failurePhase,omitempty" gorm:"size:32"`
	RollbackStatus   string     `json:"rollbackStatus" gorm:"size:32;not null"`
	RecoveryStatus   string     `json:"recoveryStatus,omitempty" gorm:"size:32"`
	RecoveryMessage  string     `json:"recoveryMessage,omitempty" gorm:"size:512"`
	RequestedBy      int64      `json:"requestedBy" gorm:"not null;index:idx_software_task_user_created"`
	CancelRequested  bool       `json:"cancelRequested" gorm:"not null;default:false"`
	EventSeq         int64      `json:"eventSeq" gorm:"not null;default:0"`
	LogPath          string     `json:"-" gorm:"size:1024"`
	ParametersJSON   string     `json:"-" gorm:"type:text"`
	SecretCiphertext string     `json:"-" gorm:"type:text"`
	SecretConsumedAt *time.Time `json:"-"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	HeartbeatAt      *time.Time `json:"heartbeatAt,omitempty"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt" gorm:"index:idx_software_task_component_created,priority:2;index:idx_software_task_status_created,priority:2;index:idx_software_task_user_created,priority:2"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

func (SoftwareTask) TableName() string {
	return "software_task"
}

type SoftwareTaskEvent struct {
	ID            uint64    `json:"-" gorm:"primaryKey;autoIncrement"`
	TaskID        string    `json:"taskId" gorm:"size:36;not null;uniqueIndex:idx_software_task_event_seq,priority:1;index:idx_software_task_event_created"`
	Seq           int64     `json:"seq" gorm:"not null;uniqueIndex:idx_software_task_event_seq,priority:2"`
	Type          string    `json:"type" gorm:"size:32;not null"`
	Level         string    `json:"level" gorm:"size:16;not null"`
	Status        string    `json:"status" gorm:"size:32"`
	Phase         string    `json:"phase" gorm:"size:32"`
	PhaseProgress *int      `json:"phaseProgress,omitempty"`
	Progress      int       `json:"progress"`
	Code          string    `json:"code,omitempty" gorm:"size:64"`
	Message       string    `json:"message" gorm:"size:512"`
	CreatedAt     time.Time `json:"createdAt" gorm:"index:idx_software_task_event_created,priority:2"`
}

func (SoftwareTaskEvent) TableName() string {
	return "software_task_event"
}

type ComponentOperationLock struct {
	Component   string    `json:"-" gorm:"primaryKey;size:64"`
	TaskID      string    `json:"-" gorm:"size:36;not null;uniqueIndex"`
	AcquiredAt  time.Time `json:"-"`
	HeartbeatAt time.Time `json:"-"`
}

type RuntimeGroupOperationLock struct {
	RuntimeGroup string    `json:"-" gorm:"primaryKey;size:64"`
	TaskID       string    `json:"-" gorm:"size:36;not null;uniqueIndex"`
	AcquiredAt   time.Time `json:"-"`
	HeartbeatAt  time.Time `json:"-"`
}

func (RuntimeGroupOperationLock) TableName() string {
	return "runtime_group_operation_lock"
}

func (ComponentOperationLock) TableName() string {
	return "component_operation_lock"
}

func IsSoftwareTaskTerminal(status string) bool {
	switch status {
	case SoftwareTaskStatusSucceeded,
		SoftwareTaskStatusFailed,
		SoftwareTaskStatusCanceled,
		SoftwareTaskStatusInterrupted:
		return true
	default:
		return false
	}
}

func ActiveSoftwareTaskStatuses() []string {
	return []string{
		SoftwareTaskStatusQueued,
		SoftwareTaskStatusResolving,
		SoftwareTaskStatusPrechecking,
		SoftwareTaskStatusInstalling,
		SoftwareTaskStatusUpgrading,
		SoftwareTaskStatusUninstalling,
		SoftwareTaskStatusStarting,
		SoftwareTaskStatusStopping,
		SoftwareTaskStatusRestarting,
		SoftwareTaskStatusReloading,
		SoftwareTaskStatusConfiguring,
		SoftwareTaskStatusVerifying,
		SoftwareTaskStatusFinalizing,
		SoftwareTaskStatusCanceling,
		SoftwareTaskStatusRollback,
	}
}
