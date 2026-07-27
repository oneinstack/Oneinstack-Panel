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
	ID         uint64     `gorm:"primaryKey" json:"id"`
	RuleID     uint       `gorm:"index;not null" json:"ruleId"`
	RuleName   string     `gorm:"size:120;index;not null" json:"ruleName"`
	Metric     string     `gorm:"size:32;index;not null" json:"metric"`
	Severity   string     `gorm:"size:16;index;not null" json:"severity"`
	EventType  string     `gorm:"size:16;index;not null" json:"eventType"`
	Value      float64    `json:"value"`
	Threshold  float64    `json:"threshold"`
	StartedAt  time.Time  `gorm:"index;not null" json:"startedAt"`
	OccurredAt time.Time  `gorm:"index;not null" json:"occurredAt"`
	ResolvedAt *time.Time `gorm:"index" json:"resolvedAt,omitempty"`
	Message    string     `gorm:"size:255" json:"message"`
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
