package models

import (
	"time"

	"gorm.io/gorm"
)

type Softwaren struct {
	gorm.Model
	Name        string    `gorm:"size:100;uniqueIndex" json:"name"`
	Description string    `gorm:"size:255" json:"description"`
	Tags        string    `gorm:"size:255" json:"tags"`
	Versions    []Version `json:"versions"`
}

type Version struct {
	gorm.Model
	SoftwareID    uint          `gorm:"index" json:"software_id"`
	Version       string        `gorm:"size:50;index" json:"version"`
	VersionName   string        `gorm:"size:100" json:"version_name"`
	DownloadURL   string        `gorm:"type:text" json:"download_url"`
	InstallConfig InstallConfig `gorm:"foreignKey:VersionID" json:"install_config"`
	InstallTime   time.Time     `gorm:"" json:"install_time"`
	HasUpdate     bool          `gorm:"" json:"has_update"`
	IsInstalled   bool          `gorm:"" json:"is_installed"`
}

type InstallConfig struct {
	gorm.Model
	VersionID       uint             `gorm:"index" json:"version_id"`
	BasePath        string           `gorm:"size:255" json:"base_path"`
	ConfigParams    []ConfigParam    `gorm:"foreignKey:InstallConfigID" json:"config_params"`
	ServiceConfig   ServiceConfig    `gorm:"foreignKey:InstallConfigID" json:"service_config"`
	ConfigTemplates []ConfigTemplate `gorm:"foreignKey:InstallConfigID" json:"config_templates"`
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
