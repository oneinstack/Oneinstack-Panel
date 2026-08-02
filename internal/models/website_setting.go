package models

import "time"

// WebsiteSetting contains structured, per-site runtime options. Free-form
// values are validated and rendered by the website service before publication.
type WebsiteSetting struct {
	WebsiteID         int64     `json:"website_id" gorm:"primaryKey"`
	RunningDirectory  string    `json:"running_directory" gorm:"size:512"`
	DirectoryListing  bool      `json:"directory_listing" gorm:"not null;default:false"`
	DefaultDocuments  string    `json:"default_documents" gorm:"size:512;not null;default:'index.php index.html index.htm'"`
	AllowedIPs        string    `json:"allowed_ips" gorm:"type:text"`
	DeniedIPs         string    `json:"denied_ips" gorm:"type:text"`
	RateLimitKB       int64     `json:"rate_limit_kb" gorm:"not null;default:0"`
	RateLimitAfterKB  int64     `json:"rate_limit_after_kb" gorm:"not null;default:0"`
	RewriteRules      string    `json:"rewrite_rules" gorm:"type:text"`
	BindingsJSON      string    `json:"-" gorm:"type:text"`
	RedirectsJSON     string    `json:"-" gorm:"type:text"`
	ProxyRulesJSON    string    `json:"-" gorm:"type:text"`
	HotlinkEnabled    bool      `json:"hotlink_enabled" gorm:"not null;default:false"`
	HotlinkAllowEmpty bool      `json:"hotlink_allow_empty" gorm:"not null"`
	HotlinkDomains    string    `json:"hotlink_domains" gorm:"type:text"`
	HotlinkExtensions string    `json:"hotlink_extensions" gorm:"size:512"`
	SecurityHeaders   bool      `json:"security_headers" gorm:"not null"`
	DeniedPaths       string    `json:"denied_paths" gorm:"type:text"`
	PHPBackend        string    `json:"php_backend" gorm:"size:512"`
	TamperProtection  bool      `json:"tamper_protection" gorm:"not null;default:false"`
	TrafficAlert      bool      `json:"traffic_alert" gorm:"not null;default:false"`
	TrafficAlertBytes int64     `json:"traffic_alert_bytes" gorm:"not null;default:0"`
	AccessLogEnabled  bool      `json:"access_log_enabled" gorm:"not null"`
	ErrorLogEnabled   bool      `json:"error_log_enabled" gorm:"not null"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func (WebsiteSetting) TableName() string {
	return "website_setting"
}
