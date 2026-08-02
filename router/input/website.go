package input

type WebsiteQueryParam struct {
	Page
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Domain  string `json:"domain"`
	Dir     string `json:"dir"`
	Remark  string `json:"remark"`
	RootDir string `json:"root_dir"`
	TarUrl  string `json:"tar_url"`
	SendUrl string `json:"send_url"`
	Class   string `json:"class"`
	Type    string `json:"type"`
}

type CertificateIssueParam struct {
	WebsiteID       int64  `json:"websiteId"`
	Email           string `json:"email"`
	AutoRenew       *bool  `json:"autoRenew"`
	RenewBeforeDays int    `json:"renewBeforeDays"`
	ForceHTTPS      bool   `json:"forceHttps"`
}

type CertificateDisableParam struct {
	ConfirmDomain string `json:"confirmDomain"`
}

type WebsiteBackupParam struct {
	WebsiteID  int64 `json:"websiteId"`
	DatabaseID int64 `json:"databaseId"`
}

type WebsiteRestoreParam struct {
	BackupID    string `json:"backupId"`
	ConfirmName string `json:"confirmName"`
}

type WebsiteDeleteParam struct {
	ID          int64  `json:"id"`
	DatabaseID  int64  `json:"databaseId"`
	DeleteFiles bool   `json:"deleteFiles"`
	ConfirmName string `json:"confirmName"`
}

type WebsiteStatusParam struct {
	Enabled bool `json:"enabled"`
}

type DeleteWebsiteBackupParam struct {
	ConfirmName string `json:"confirmName"`
}
