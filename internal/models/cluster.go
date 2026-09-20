package models

import (
	"time"

	"gorm.io/gorm"
)

const (
	ClusterNodeStatusPending = "pending"
	ClusterNodeStatusOnline  = "online"
	ClusterNodeStatusOffline = "offline"
	ClusterNodeStatusError   = "error"

	ClusterNodeLifecycleActive        = "active"
	ClusterNodeLifecycleMaintenance   = "maintenance"
	ClusterNodeLifecycleDraining      = "draining"
	ClusterNodeLifecycleDrained       = "drained"
	ClusterNodeLifecycleDisabled      = "disabled"
	ClusterNodeLifecyclePendingDelete = "pending_delete"
)

type ClusterMetricLevel struct {
	Value             float64 `json:"value"`
	Level             string  `json:"level"`
	WarningThreshold  float64 `json:"warningThreshold"`
	CriticalThreshold float64 `json:"criticalThreshold"`
}

type ClusterMetricHealth struct {
	CPU    ClusterMetricLevel `json:"cpu"`
	Memory ClusterMetricLevel `json:"memory"`
	Disk   ClusterMetricLevel `json:"disk"`
}

// ClusterNode describes another OneinStack Panel managed by this Panel.
// The agent token is never returned or serialized; only its SHA-256 hash is
// persisted in the database.
type ClusterNode struct {
	ID                       uint                `gorm:"primaryKey" json:"id"`
	Name                     string              `gorm:"size:120;not null" json:"name"`
	Endpoint                 string              `gorm:"size:512;not null" json:"endpoint"`
	TokenHash                string              `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Enabled                  bool                `gorm:"index;not null;default:true" json:"enabled"`
	Group                    string              `gorm:"size:120;index" json:"group,omitempty"`
	Tags                     string              `gorm:"size:512" json:"tags,omitempty"`
	Status                   string              `gorm:"size:16;index;not null;default:pending" json:"status"`
	LifecycleStatus          string              `gorm:"size:24;index;not null;default:active" json:"lifecycleStatus"`
	ConnectionStatus         string              `gorm:"-" json:"connectionStatus"`
	EffectiveStatus          string              `gorm:"-" json:"effectiveStatus"`
	EndpointAddressMismatch  bool                `gorm:"-" json:"endpointAddressMismatch"`
	MetricHealth             ClusterMetricHealth `gorm:"-" json:"metricHealth"`
	LastRegisteredAt         *time.Time          `gorm:"index" json:"lastRegisteredAt,omitempty"`
	LastSeenAt               *time.Time          `gorm:"index" json:"lastSeenAt,omitempty"`
	DepartedAt               *time.Time          `gorm:"index" json:"-"`
	LastError                string              `gorm:"size:512" json:"lastError,omitempty"`
	Hostname                 string              `gorm:"size:255" json:"hostname,omitempty"`
	SystemID                 string              `gorm:"size:255" json:"systemId,omitempty"`
	SystemVersion            string              `gorm:"size:120" json:"systemVersion,omitempty"`
	Architecture             string              `gorm:"size:64" json:"architecture,omitempty"`
	PanelVersion             string              `gorm:"size:120" json:"panelVersion,omitempty"`
	AgentVersion             string              `gorm:"size:120" json:"agentVersion,omitempty"`
	Capabilities             []string            `gorm:"serializer:json;type:text" json:"capabilities,omitempty"`
	HeartbeatIntervalSeconds int                 `gorm:"not null;default:30" json:"heartbeatIntervalSeconds"`
	CPUPercent               float64             `json:"cpuPercent"`
	MemoryPercent            float64             `json:"memoryPercent"`
	DiskPercent              float64             `json:"diskPercent"`
	NetworkRecvBPS           float64             `json:"networkReceiveBps"`
	NetworkSendBPS           float64             `json:"networkSendBps"`
	UptimeSeconds            uint64              `json:"uptimeSeconds"`
	CPUTotalCores            int                 `json:"cpuTotalCores"`
	CPUUsedCores             float64             `json:"cpuUsedCores"`
	MemoryUsedBytes          uint64              `json:"memoryUsedBytes"`
	MemoryTotalBytes         uint64              `json:"memoryTotalBytes"`
	DiskUsedBytes            uint64              `json:"diskUsedBytes"`
	DiskTotalBytes           uint64              `json:"diskTotalBytes"`
	IPAddress                string              `gorm:"size:64" json:"ipAddress,omitempty"`
	SubnetMask               string              `gorm:"size:64" json:"subnetMask,omitempty"`
	Gateway                  string              `gorm:"size:64" json:"gateway,omitempty"`
	MACAddress               string              `gorm:"size:64" json:"macAddress,omitempty"`
	InterfaceName            string              `gorm:"size:64" json:"interfaceName,omitempty"`
	CreatedAt                time.Time           `json:"createdAt"`
	UpdatedAt                time.Time           `json:"updatedAt"`
	DeletedAt                gorm.DeletedAt      `gorm:"index" json:"-"`
}

// ClusterNodeMetric stores a point-in-time resource snapshot reported by an
// agent. Retention can be applied by a scheduled cleanup job later.
type ClusterNodeMetric struct {
	ID             uint64    `gorm:"primaryKey" json:"id"`
	NodeID         uint      `gorm:"index;not null" json:"nodeId"`
	CapturedAt     time.Time `gorm:"index;not null" json:"capturedAt"`
	CPUPercent     float64   `json:"cpuPercent"`
	MemoryPercent  float64   `json:"memoryPercent"`
	DiskPercent    float64   `json:"diskPercent"`
	NetworkRecvBPS float64   `json:"networkReceiveBps"`
	NetworkSendBPS float64   `json:"networkSendBps"`
	UptimeSeconds  uint64    `json:"uptimeSeconds"`
}

// ClusterPolicy is a singleton controller-side policy. Targets are structured
// values only; they are never interpreted as shell fragments.
type ClusterPolicy struct {
	ID                      uint      `gorm:"primaryKey" json:"id"`
	CPUWarningThreshold     float64   `gorm:"not null;default:80" json:"cpuWarningThreshold"`
	CPUCriticalThreshold    float64   `gorm:"not null;default:90" json:"cpuCriticalThreshold"`
	MemoryWarningThreshold  float64   `gorm:"not null;default:80" json:"memoryWarningThreshold"`
	MemoryCriticalThreshold float64   `gorm:"not null;default:90" json:"memoryCriticalThreshold"`
	DiskWarningThreshold    float64   `gorm:"not null;default:80" json:"diskWarningThreshold"`
	DiskCriticalThreshold   float64   `gorm:"not null;default:90" json:"diskCriticalThreshold"`
	DNSTargets              []string  `gorm:"serializer:json;type:text" json:"dnsTargets"`
	NetworkTargets          []string  `gorm:"serializer:json;type:text" json:"networkTargets"`
	CommonPorts             []int     `gorm:"serializer:json;type:text" json:"commonPorts"`
	ExtraServices           []string  `gorm:"serializer:json;type:text" json:"extraServices"`
	LogLookbackMinutes      int       `gorm:"not null;default:30" json:"logLookbackMinutes"`
	MaxLogEntries           int       `gorm:"not null;default:200" json:"maxLogEntries"`
	DiagnosisConcurrency    int       `gorm:"not null;default:10" json:"diagnosisConcurrency"`
	LifecycleConcurrency    int       `gorm:"not null;default:10" json:"lifecycleConcurrency"`
	RestartConcurrency      int       `gorm:"not null;default:3" json:"restartConcurrency"`
	UpdateConcurrency       int       `gorm:"not null;default:1" json:"updateConcurrency"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}
