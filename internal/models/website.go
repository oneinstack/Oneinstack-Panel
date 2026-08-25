package models

import (
	"time"

	"gorm.io/gorm"
)

type Website struct {
	ID                   int64      `json:"id"`
	Name                 string     `json:"name"`
	Domain               string     `json:"domain"`
	Dir                  string     `json:"dir"`
	Remark               string     `json:"remark"`
	RootDir              string     `json:"root_dir"`
	TarUrl               string     `json:"tar_url"`
	SendUrl              string     `json:"send_url"`
	Class                string     `json:"class"`
	Type                 string     `json:"type"`
	Engine               string     `json:"engine,omitempty" gorm:"size:32;index"`
	Pact                 string     `json:"pact"`
	CreateTime           time.Time  `json:"create_time"`
	UpdateTime           time.Time  `json:"update_time"`
	Enabled              bool       `json:"enabled" gorm:"not null;default:true;index"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty" gorm:"index"`
	DisabledReason       string     `json:"disabled_reason,omitempty" gorm:"size:32"`
	TodayTrafficBytes    int64      `json:"today_traffic_bytes" gorm:"-"`
	TodayRequests        int64      `json:"today_requests" gorm:"-"`
	SSLEnabled           bool       `json:"ssl_enabled" gorm:"-"`
	CertificateStatus    string     `json:"certificate_status,omitempty" gorm:"-"`
	CertificateExpiresAt *time.Time `json:"certificate_expires_at,omitempty" gorm:"-"`
}

// WebsiteTrafficDaily stores the response traffic parsed incrementally from a
// website's dedicated access log. Day uses the web server's local log date.
type WebsiteTrafficDaily struct {
	ID           int64     `json:"id"`
	WebsiteID    int64     `json:"website_id" gorm:"not null;uniqueIndex:idx_website_traffic_day"`
	Day          string    `json:"day" gorm:"size:10;not null;uniqueIndex:idx_website_traffic_day"`
	BytesSent    int64     `json:"bytes_sent" gorm:"not null;default:0"`
	RequestCount int64     `json:"request_count" gorm:"not null;default:0"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (WebsiteTrafficDaily) TableName() string {
	return "website_traffic_daily"
}

// WebsiteTrafficCursor prevents full access-log rescans. FileIdentity changes
// when logrotate replaces a file, so the collector safely resumes at offset 0.
type WebsiteTrafficCursor struct {
	WebsiteID    int64     `json:"website_id" gorm:"primaryKey"`
	LogPath      string    `json:"log_path" gorm:"size:1024;not null"`
	FileIdentity string    `json:"file_identity" gorm:"size:128;not null"`
	Offset       int64     `json:"offset" gorm:"not null;default:0"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (WebsiteTrafficCursor) TableName() string {
	return "website_traffic_cursor"
}

func (m *Website) BeforeCreate(tx *gorm.DB) (err error) {
	m.CreateTime = time.Now()
	return
}

func (m *Website) BeforeUpdate(tx *gorm.DB) (err error) {
	m.UpdateTime = time.Now()
	return
}
