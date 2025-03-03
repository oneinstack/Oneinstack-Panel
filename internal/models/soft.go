package models

import (
	"time"

	"gorm.io/gorm"
)

type Softwaren struct {
	gorm.Model
	Name         string    `gorm:"size:100;uniqueIndex" json:"name"`
	Description  string    `gorm:"size:255" json:"description"`
	Tags         string    `gorm:"size:255" json:"tags"`
	Versions     []Version `gorm:"foreignKey:SoftwareID" json:"versions"`
	HasUpdate    bool      `gorm:"" json:"has_update"`
	LastSyncedAt time.Time `gorm:"index" json:"last_synced_at"`
}

func (Softwaren) TableName() string {
	return "softwarens"
}

type Version struct {
	gorm.Model
	SoftwareID    uint          `gorm:"index:idx_software_version,priority:1" json:"software_id"`
	Version       string        `gorm:"index:idx_software_version,priority:2" json:"version"`
	VersionName   string        `gorm:"size:100" json:"version_name"`
	DownloadURL   string        `gorm:"type:text" json:"download_url"`
	InstallConfig InstallConfig `gorm:"foreignKey:VersionID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"install_config"`
	InstallTime   time.Time     `gorm:"" json:"install_time"`
	IsInstalled   bool          `gorm:"" json:"is_installed"`
	InstallPath   string        `gorm:"size:255" json:"install_path"`
	InstallParams string        `gorm:"type:text" json:"install_params"`
	InstallLog    string        `gorm:"type:text" json:"install_log"`
}

type InstallConfig struct {
	gorm.Model
	VersionID       uint             `gorm:"index" json:"version_id"`
	BasePath        string           `gorm:"size:255" json:"base_path"`
	ConfigParams    []ConfigParam    `gorm:"foreignKey:InstallConfigID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"config_params"`
	ServiceConfig   ServiceConfig    `gorm:"foreignKey:InstallConfigID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"service_config"`
	ConfigTemplates []ConfigTemplate `gorm:"foreignKey:InstallConfigID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE;" json:"config_templates"`
	Cmd             string           `gorm:"size:100" json:"cmd"`
}

type ConfigParam struct {
	gorm.Model
	InstallConfigID uint   `gorm:"index" json:"install_config_id"`
	ConfigFile      string `gorm:"size:100" json:"config_file"` // 例如：php.ini, redis.conf
	Name            string `gorm:"size:100" json:"name"`
	Type            string `gorm:"size:50" json:"type"`     // string/int/bool
	DefaultValue    string `gorm:"size:255" json:"default"` // 默认值
	Description     string `gorm:"size:255" json:"description"`
	Required        bool   `json:"required"`
	Sensitive       bool   `json:"sensitive"` // 是否敏感信息
}

type ServiceConfig struct {
	gorm.Model
	InstallConfigID uint   `gorm:"index" json:"install_config_id"`
	StartCmd        string `gorm:"type:text" json:"start_cmd"`
	ReloadCmd       string `gorm:"type:text" json:"reload_cmd"`
	StopCmd         string `gorm:"type:text" json:"stop_cmd"`
	SystemdTemplate string `gorm:"type:text" json:"systemd_template"`
}

type ConfigTemplate struct {
	gorm.Model
	InstallConfigID uint   `gorm:"index" json:"install_config_id"`
	FileName        string `gorm:"size:100" json:"file_name"` // 例如：Caddyfile
	Content         string `gorm:"type:longtext" json:"content"`
}
