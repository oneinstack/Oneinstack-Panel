package models

import "time"

const (
	MonitorStateNormal  = "normal"
	MonitorStatePending = "pending"
	MonitorStateFiring  = "firing"

	AlertEventTriggered = "triggered"
	AlertEventReminder  = "reminder"
	AlertEventResolved  = "resolved"
)

type MetricSample struct {
	ID                uint64    `gorm:"primaryKey" json:"id"`
	CapturedAt        time.Time `gorm:"index;not null" json:"capturedAt"`
	CPUPercent        float64   `json:"cpuPercent"`
	MemoryPercent     float64   `json:"memoryPercent"`
	DiskPercent       float64   `json:"diskPercent"`
	Load1             float64   `json:"load1"`
	Load5             float64   `json:"load5"`
	Load15            float64   `json:"load15"`
	NetworkReceiveBPS float64   `json:"networkReceiveBps"`
	NetworkSendBPS    float64   `json:"networkSendBps"`
	DiskReadBPS       float64   `json:"diskReadBps"`
	DiskWriteBPS      float64   `json:"diskWriteBps"`
}

type MonitorRule struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Name               string     `gorm:"size:120;not null" json:"name"`
	Metric             string     `gorm:"size:32;index;not null" json:"metric"`
	Operator           string     `gorm:"size:8;not null" json:"operator"`
	Threshold          float64    `json:"threshold"`
	RecoveryThreshold  float64    `json:"recoveryThreshold"`
	ConsecutiveSamples int        `gorm:"not null" json:"consecutiveSamples"`
	CooldownMinutes    int        `gorm:"not null" json:"cooldownMinutes"`
	Severity           string     `gorm:"size:16;index;not null" json:"severity"`
	Enabled            bool       `gorm:"index;not null" json:"enabled"`
	SilencedUntil      *time.Time `gorm:"index" json:"silencedUntil,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	CurrentState       string     `gorm:"-" json:"state"`
	LastValue          float64    `gorm:"-" json:"lastValue"`
	LastEvaluatedAt    *time.Time `gorm:"-" json:"lastEvaluatedAt,omitempty"`
	FiringSince        *time.Time `gorm:"-" json:"firingSince,omitempty"`
}

type MonitorAlertState struct {
	RuleID              uint       `gorm:"primaryKey" json:"ruleId"`
	State               string     `gorm:"size:16;index;not null" json:"state"`
	ConsecutiveBreaches int        `gorm:"not null" json:"consecutiveBreaches"`
	LastValue           float64    `json:"lastValue"`
	PendingSince        *time.Time `json:"pendingSince,omitempty"`
	FiringSince         *time.Time `json:"firingSince,omitempty"`
	LastEvaluatedAt     time.Time  `gorm:"index" json:"lastEvaluatedAt"`
	LastNotifiedAt      *time.Time `json:"lastNotifiedAt,omitempty"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type MonitorAlertEvent struct {
	ID           uint64     `gorm:"primaryKey" json:"id"`
	RuleID       uint       `gorm:"index;not null" json:"ruleId"`
	RuleName     string     `gorm:"size:120;index;not null" json:"ruleName"`
	Metric       string     `gorm:"size:32;index;not null" json:"metric"`
	ResourceType string     `gorm:"size:32;index" json:"resourceType,omitempty"`
	ResourceID   string     `gorm:"size:64;index" json:"resourceId,omitempty"`
	Severity     string     `gorm:"size:16;index;not null" json:"severity"`
	EventType    string     `gorm:"size:16;index;not null" json:"eventType"`
	Value        float64    `json:"value"`
	Threshold    float64    `json:"threshold"`
	StartedAt    time.Time  `gorm:"index;not null" json:"startedAt"`
	OccurredAt   time.Time  `gorm:"index;not null" json:"occurredAt"`
	ResolvedAt   *time.Time `gorm:"index" json:"resolvedAt,omitempty"`
	Message      string     `gorm:"size:255" json:"message"`
}

// ComponentHealthState stores the durable service health state independently
// from user-created numeric monitor rules. Installed services are evaluated by
// consecutive probes, support silence windows, and generate normal monitor
// events so existing notification channels can deliver them.
type ComponentHealthState struct {
	Component           string     `gorm:"primaryKey;size:64" json:"component"`
	DisplayName         string     `gorm:"size:120;not null" json:"displayName"`
	SoftwareKey         string     `gorm:"size:64;not null" json:"softwareKey"`
	ServiceName         string     `gorm:"size:120;not null" json:"serviceName"`
	SoftwareVersion     string     `gorm:"size:64" json:"softwareVersion,omitempty"`
	RuntimeVersion      string     `gorm:"size:64" json:"runtimeVersion,omitempty"`
	Installed           bool       `gorm:"index;not null" json:"installed"`
	Busy                bool       `gorm:"index;not null" json:"busy"`
	HealthState         string     `gorm:"size:16;index;not null" json:"healthState"`
	ServiceState        string     `gorm:"size:32;index;not null" json:"serviceState"`
	LoadState           string     `gorm:"size:32" json:"loadState,omitempty"`
	ActiveState         string     `gorm:"size:32" json:"activeState,omitempty"`
	SubState            string     `gorm:"size:32" json:"subState,omitempty"`
	ConsecutiveFailures int        `gorm:"not null" json:"consecutiveFailures"`
	LastError           string     `gorm:"size:512" json:"lastError,omitempty"`
	LastCheckedAt       time.Time  `gorm:"index;not null" json:"lastCheckedAt"`
	PendingSince        *time.Time `json:"pendingSince,omitempty"`
	FiringSince         *time.Time `json:"firingSince,omitempty"`
	LastNotifiedAt      *time.Time `json:"lastNotifiedAt,omitempty"`
	SilencedUntil       *time.Time `gorm:"index" json:"silencedUntil,omitempty"`
	CreatedAt           time.Time  `json:"createdAt"`
	UpdatedAt           time.Time  `json:"updatedAt"`
}

type NotificationChannel struct {
	ID              string    `gorm:"primaryKey;size:64" json:"id"`
	Name            string    `gorm:"size:120;not null" json:"name"`
	Type            string    `gorm:"size:24;index;not null" json:"type"`
	Enabled         bool      `gorm:"index;not null" json:"enabled"`
	ConfigEncrypted string    `gorm:"type:text;not null" json:"-"`
	TargetHint      string    `gorm:"size:160" json:"targetHint"`
	HasSecret       bool      `json:"hasSecret"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type NotificationDelivery struct {
	ID          uint64    `gorm:"primaryKey" json:"id"`
	EventID     uint64    `gorm:"index;not null" json:"eventId"`
	ChannelID   string    `gorm:"size:64;index;not null" json:"channelId"`
	ChannelName string    `gorm:"size:120" json:"channelName"`
	Status      string    `gorm:"size:16;index;not null" json:"status"`
	Error       string    `gorm:"size:255" json:"error,omitempty"`
	AttemptedAt time.Time `gorm:"index;not null" json:"attemptedAt"`
}
