package models

import "time"

const (
	ClusterNodeStatusPending = "pending"
	ClusterNodeStatusOnline  = "online"
	ClusterNodeStatusOffline = "offline"
	ClusterNodeStatusError   = "error"
)

// ClusterNode describes another OneinStack Panel managed by this Panel.
// The agent token is never returned or serialized; only its SHA-256 hash is
// persisted in the database.
type ClusterNode struct {
	ID                       uint       `gorm:"primaryKey" json:"id"`
	Name                     string     `gorm:"size:120;not null" json:"name"`
	Endpoint                 string     `gorm:"size:512;not null" json:"endpoint"`
	TokenHash                string     `gorm:"size:64;uniqueIndex;not null" json:"-"`
	Enabled                  bool       `gorm:"index;not null;default:true" json:"enabled"`
	Group                    string     `gorm:"size:120;index" json:"group,omitempty"`
	Tags                     string     `gorm:"size:512" json:"tags,omitempty"`
	Status                   string     `gorm:"size:16;index;not null;default:pending" json:"status"`
	LastRegisteredAt         *time.Time `gorm:"index" json:"lastRegisteredAt,omitempty"`
	LastSeenAt               *time.Time `gorm:"index" json:"lastSeenAt,omitempty"`
	DepartedAt               *time.Time `gorm:"index" json:"-"`
	LastError                string     `gorm:"size:512" json:"lastError,omitempty"`
	Hostname                 string     `gorm:"size:255" json:"hostname,omitempty"`
	SystemID                 string     `gorm:"size:255" json:"systemId,omitempty"`
	SystemVersion            string     `gorm:"size:120" json:"systemVersion,omitempty"`
	Architecture             string     `gorm:"size:64" json:"architecture,omitempty"`
	PanelVersion             string     `gorm:"size:120" json:"panelVersion,omitempty"`
	AgentVersion             string     `gorm:"size:120" json:"agentVersion,omitempty"`
	Capabilities             []string   `gorm:"serializer:json;type:text" json:"capabilities,omitempty"`
	HeartbeatIntervalSeconds int        `gorm:"not null;default:30" json:"heartbeatIntervalSeconds"`
	CPUPercent               float64    `json:"cpuPercent"`
	MemoryPercent            float64    `json:"memoryPercent"`
	DiskPercent              float64    `json:"diskPercent"`
	NetworkRecvBPS           float64    `json:"networkReceiveBps"`
	NetworkSendBPS           float64    `json:"networkSendBps"`
	UptimeSeconds            uint64     `json:"uptimeSeconds"`
	CPUTotalCores            int        `json:"cpuTotalCores"`
	CPUUsedCores             float64    `json:"cpuUsedCores"`
	MemoryUsedBytes          uint64     `json:"memoryUsedBytes"`
	MemoryTotalBytes         uint64     `json:"memoryTotalBytes"`
	DiskUsedBytes            uint64     `json:"diskUsedBytes"`
	DiskTotalBytes           uint64     `json:"diskTotalBytes"`
	IPAddress                string     `gorm:"size:64" json:"ipAddress,omitempty"`
	SubnetMask               string     `gorm:"size:64" json:"subnetMask,omitempty"`
	Gateway                  string     `gorm:"size:64" json:"gateway,omitempty"`
	MACAddress               string     `gorm:"size:64" json:"macAddress,omitempty"`
	InterfaceName            string     `gorm:"size:64" json:"interfaceName,omitempty"`
	CreatedAt                time.Time  `json:"createdAt"`
	UpdatedAt                time.Time  `json:"updatedAt"`
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
