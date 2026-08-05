package models

import "time"

const (
	BastionStatusUnknown = "unknown"
	BastionStatusOnline  = "online"
	BastionStatusOffline = "offline"
	BastionStatusError   = "error"

	BastionAuthPassword = "password"
	BastionAuthKey      = "key"
)

// BastionServer 远端服务器注册信息
type BastionServer struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	Name          string     `gorm:"size:120;not null" json:"name"`
	Host          string     `gorm:"size:255;not null" json:"host"`
	Port          int        `gorm:"not null;default:22" json:"port"`
	Username      string     `gorm:"size:64;not null" json:"username"`
	AuthMethod    string     `gorm:"size:16;not null;default:password" json:"authMethod"`
	PasswordEnc   string     `gorm:"type:text" json:"-"`
	KeyPath       string     `gorm:"size:512" json:"-"`
	KeyConfigured bool       `gorm:"-" json:"keyConfigured"`
	Tags          string     `gorm:"size:255" json:"tags"`
	Enabled       bool       `gorm:"index;not null;default:true" json:"enabled"`
	LastSeenAt    *time.Time `gorm:"index" json:"lastSeenAt,omitempty"`
	Status        string     `gorm:"size:16;index;not null;default:unknown" json:"status"`
	StatusError   string     `gorm:"size:512" json:"statusError,omitempty"`
	OSInfo        string     `gorm:"size:255" json:"osInfo,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// BastionMetricSample 远端服务器采集快照
type BastionMetricSample struct {
	ID                uint64    `gorm:"primaryKey" json:"id"`
	ServerID          uint      `gorm:"index;not null" json:"serverId"`
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
	UptimeSeconds     uint64    `json:"uptimeSeconds"`
}

// BastionServerSummary 服务器列表摘要（含最新采集数据）
type BastionServerSummary struct {
	BastionServer
	LatestCPU         *float64   `json:"latestCpu,omitempty"`
	LatestMemory      *float64   `json:"latestMemory,omitempty"`
	LatestDisk        *float64   `json:"latestDisk,omitempty"`
	LatestNetworkRecv *float64   `json:"latestNetworkRecv,omitempty"`
	LatestNetworkSend *float64   `json:"latestNetworkSend,omitempty"`
	LatestCapturedAt  *time.Time `json:"latestCapturedAt,omitempty"`
}
