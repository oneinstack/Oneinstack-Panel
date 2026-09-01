package models

import "time"

const (
	Fail2banTaskQueued      = "queued"
	Fail2banTaskRunning     = "running"
	Fail2banTaskSucceeded   = "succeeded"
	Fail2banTaskFailed      = "failed"
	Fail2banTaskInterrupted = "interrupted"
)

type Fail2banPolicy struct {
	ID              string     `json:"id" gorm:"primaryKey;size:36"`
	Template        string     `json:"template" gorm:"size:32;not null;index"`
	Name            string     `json:"name" gorm:"size:64;not null"`
	Enabled         bool       `json:"enabled" gorm:"not null;default:false"`
	EnforcementMode string     `json:"enforcementMode" gorm:"size:16;not null;default:observe"`
	MaxRetry        int        `json:"maxRetry" gorm:"not null"`
	FindTimeSeconds int        `json:"findTimeSeconds" gorm:"not null"`
	BanTimeSeconds  int        `json:"banTimeSeconds" gorm:"not null"`
	IgnoreIPs       []string   `json:"ignoreIps" gorm:"serializer:json;type:text"`
	JailName        string     `json:"jailName" gorm:"size:96;not null;uniqueIndex"`
	DetectorJail    string     `json:"detectorJail,omitempty" gorm:"size:96"`
	Revision        string     `json:"revision" gorm:"size:64;not null"`
	CreatedBy       int64      `json:"createdBy" gorm:"not null"`
	UpdatedBy       int64      `json:"updatedBy" gorm:"not null"`
	LastAppliedAt   *time.Time `json:"lastAppliedAt,omitempty"`
	LastApplyError  string     `json:"lastApplyError,omitempty" gorm:"size:512"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (Fail2banPolicy) TableName() string { return "fail2ban_policy" }

// Fail2banBan records the expiry deadline for a managed ban. Fail2ban keeps
// its own runtime state, while this record lets Panel recover and retry an
// expiry unban after a Panel or Fail2ban restart.
type Fail2banBan struct {
	ID             string    `json:"-" gorm:"primaryKey;size:36"`
	PolicyID       string    `json:"policyId" gorm:"size:36;not null;uniqueIndex:idx_fail2ban_ban_policy_ip,priority:1"`
	Jail           string    `json:"jail" gorm:"size:96;not null"`
	IP             string    `json:"ip" gorm:"size:64;not null;uniqueIndex:idx_fail2ban_ban_policy_ip,priority:2"`
	BanTimeSeconds int       `json:"banTimeSeconds" gorm:"not null"`
	BannedAt       time.Time `json:"bannedAt" gorm:"not null"`
	ExpiresAt      time.Time `json:"expiresAt" gorm:"not null;index"`
	TaskID         string    `json:"taskId,omitempty" gorm:"size:36;index"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func (Fail2banBan) TableName() string { return "fail2ban_ban" }

type SecurityIncident struct {
	ID          string     `json:"id" gorm:"primaryKey;size:36"`
	PolicyID    string     `json:"policyId" gorm:"size:36;not null;index"`
	Source      string     `json:"source" gorm:"size:32;not null;index"`
	RemoteIP    string     `json:"remoteIp" gorm:"size:64;not null;index"`
	Fingerprint string     `json:"-" gorm:"size:64;not null;uniqueIndex"`
	Attempts    int        `json:"attempts" gorm:"not null"`
	Severity    string     `json:"severity" gorm:"size:16;not null"`
	Status      string     `json:"status" gorm:"size:16;not null;index"`
	Evidence    []uint64   `json:"evidence,omitempty" gorm:"serializer:json;type:text"`
	TaskID      string     `json:"taskId,omitempty" gorm:"size:36;index"`
	FirstSeenAt time.Time  `json:"firstSeenAt" gorm:"not null;index"`
	LastSeenAt  time.Time  `json:"lastSeenAt" gorm:"not null;index"`
	ResolvedAt  *time.Time `json:"resolvedAt,omitempty"`
	ResolvedBy  int64      `json:"resolvedBy,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

func (SecurityIncident) TableName() string { return "security_incident" }

type Fail2banTask struct {
	ID             string     `json:"id" gorm:"primaryKey;size:36"`
	Operation      string     `json:"operation" gorm:"size:32;not null;index"`
	PolicyID       string     `json:"policyId,omitempty" gorm:"size:36;index"`
	IncidentID     string     `json:"incidentId,omitempty" gorm:"size:36;index"`
	TargetIP       string     `json:"targetIp,omitempty" gorm:"size:64;index"`
	Status         string     `json:"status" gorm:"size:24;not null;index"`
	Phase          string     `json:"phase" gorm:"size:32;not null"`
	Progress       int        `json:"progress" gorm:"not null;default:0"`
	Message        string     `json:"message" gorm:"size:512"`
	ErrorCode      string     `json:"errorCode,omitempty" gorm:"size:64"`
	ErrorMessage   string     `json:"errorMessage,omitempty" gorm:"size:512"`
	RequestedBy    int64      `json:"requestedBy" gorm:"not null;index"`
	TriggeredBy    string     `json:"triggeredBy" gorm:"size:16;not null"`
	IdempotencyKey string     `json:"-" gorm:"size:128;index"`
	ParametersJSON string     `json:"-" gorm:"type:text;not null"`
	EventSeq       int64      `json:"eventSeq" gorm:"not null;default:0"`
	StartedAt      *time.Time `json:"startedAt,omitempty"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt" gorm:"index"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

func (Fail2banTask) TableName() string { return "fail2ban_task" }

type Fail2banTaskEvent struct {
	ID        uint64    `json:"-" gorm:"primaryKey;autoIncrement"`
	TaskID    string    `json:"taskId" gorm:"size:36;not null;uniqueIndex:idx_fail2ban_task_event_seq,priority:1"`
	Seq       int64     `json:"seq" gorm:"not null;uniqueIndex:idx_fail2ban_task_event_seq,priority:2"`
	Type      string    `json:"type" gorm:"size:24;not null"`
	Level     string    `json:"level" gorm:"size:16;not null"`
	Status    string    `json:"status" gorm:"size:24"`
	Phase     string    `json:"phase" gorm:"size:32"`
	Progress  int       `json:"progress"`
	Code      string    `json:"code,omitempty" gorm:"size:64"`
	Message   string    `json:"message" gorm:"size:512"`
	CreatedAt time.Time `json:"createdAt"`
}

func (Fail2banTaskEvent) TableName() string { return "fail2ban_task_event" }

type Fail2banState struct {
	ID              uint       `json:"-" gorm:"primaryKey"`
	AuditSequence   uint64     `json:"auditSequence" gorm:"not null;default:0"`
	EventFileOffset int64      `json:"eventFileOffset" gorm:"not null;default:0"`
	MigrationStatus string     `json:"migrationStatus" gorm:"size:24;not null;default:pending"`
	MigrationError  string     `json:"migrationError,omitempty" gorm:"size:512"`
	MigratedAt      *time.Time `json:"migratedAt,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

func (Fail2banState) TableName() string { return "fail2ban_state" }

func IsFail2banTaskTerminal(status string) bool {
	switch status {
	case Fail2banTaskSucceeded, Fail2banTaskFailed, Fail2banTaskInterrupted:
		return true
	default:
		return false
	}
}
