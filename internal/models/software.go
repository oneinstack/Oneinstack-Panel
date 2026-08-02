package models

import (
	"time"

	"gorm.io/gorm"
)

const (
	Soft_Status_Default = 0
	Soft_Status_Ing     = 1
	Soft_Status_Suc     = 2
	Soft_Status_Err     = 3
)

type Software struct {
	Id              int       `json:"id" gorm:"primaryKey;autoIncrement"`
	Name            string    `json:"name" gorm:"index:idx_name_installed_type"`
	Type            string    `json:"type" gorm:"index:idx_name_installed_type"`
	Installed       bool      `json:"installed" gorm:"index:idx_name_installed_type"`
	Key             string    `json:"key"`
	Component       string    `json:"component" gorm:"size:64;index"`
	Icon            string    `json:"icon"`
	Describe        string    `json:"describe"`
	Status          int       `json:"status"` //0待安装,1安装中,2安装成功,3安装失败
	Resource        string    `json:"resource"`
	Tags            string    `json:"tags"`
	Version         string    `json:"version"`
	Params          string    `json:"params"`
	Log             string    `json:"log"`
	Script          string    `json:"script"`
	HttpPort        string    `json:"http_prot"`
	HttpsPort       string    `json:"https_prot"`
	RootPwd         string    `json:"root_pwd"`
	UrlPath         string    `json:"url_path"`
	InstallVersion  string    `json:"install_version"`
	IsUpdate        bool      `json:"is_update"`
	CatalogManaged  bool      `json:"catalog_managed" gorm:"not null;default:false;index"`
	CatalogChannel  string    `json:"catalog_channel" gorm:"size:32;index"`
	CatalogVisible  bool      `json:"catalog_visible" gorm:"not null;default:true;index"`
	Installable     bool      `json:"installable" gorm:"not null;default:true"`
	Recommended     bool      `json:"recommended" gorm:"not null;default:false"`
	CatalogOrder    int       `json:"catalog_order" gorm:"not null;default:0;index"`
	VersionOrder    int       `json:"version_order" gorm:"not null;default:0"`
	CatalogRevision string    `json:"catalog_revision" gorm:"size:64"`
	ReleaseNotes    string    `json:"release_notes" gorm:"type:text"`
	InstallTime     time.Time `json:"install_time"`
	CreateTime      time.Time `json:"create_time"`
}

type Softwares struct {
	Software
	Versions           string `gorm:"column:versions"`
	RecommendedVersion string `gorm:"column:recommended_version"`
}

// SoftwareCatalogState records the last verified Center catalog snapshot.
// The software rows remain the offline cache; this record only describes
// provenance, freshness, and the last synchronization error.
type SoftwareCatalogState struct {
	ID            uint       `json:"-" gorm:"primaryKey"`
	Mode          string     `json:"mode" gorm:"size:32;not null"`
	Channel       string     `json:"channel,omitempty" gorm:"size:32"`
	Revision      string     `json:"revision,omitempty" gorm:"size:64"`
	KeyID         string     `json:"keyId,omitempty" gorm:"size:64"`
	ProductCount  int        `json:"productCount"`
	VersionCount  int        `json:"versionCount"`
	LastSyncedAt  *time.Time `json:"lastSyncedAt,omitempty"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty" gorm:"size:1024"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

func (SoftwareCatalogState) TableName() string {
	return "software_catalog_state"
}

func (m *Software) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}
