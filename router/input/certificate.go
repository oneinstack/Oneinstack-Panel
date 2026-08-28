package input

type CertificateListParam struct {
	Page     int `form:"page" json:"page"`
	PageSize int `form:"pageSize" json:"pageSize"`
}

type CertificateUploadParam struct {
	Domains         []string `json:"domains"`
	CertificatePEM  string   `json:"certificate"`
	PrivateKeyPEM   string   `json:"privateKey"`
	Remark          string   `json:"remark"`
	AutoRenew       bool     `json:"autoRenew"`
	RenewBeforeDays int      `json:"renewBeforeDays"`
}

type CertificateSelfSignedParam struct {
	Domains         []string `json:"domains"`
	Algorithm       string   `json:"algorithm"`
	ValidityYears   int      `json:"validityYears"`
	Remark          string   `json:"remark"`
	AutoRenew       bool     `json:"autoRenew"`
	RenewBeforeDays int      `json:"renewBeforeDays"`
}

type CertificateACMEParam struct {
	ChallengeType   string   `json:"challengeType"`
	WebsiteID       int64    `json:"websiteId"`
	Domains         []string `json:"domains"`
	Email           string   `json:"email"`
	DNSAccountID    string   `json:"dnsAccountId"`
	AutoRenew       *bool    `json:"autoRenew"`
	RenewBeforeDays int      `json:"renewBeforeDays"`
	Remark          string   `json:"remark"`
}

type CertificateBindingParam struct {
	WebsiteID  int64 `json:"websiteId"`
	ForceHTTPS bool  `json:"forceHttps"`
}

type DNSAccountParam struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	CredentialOne string `json:"credentialOne"`
	CredentialTwo string `json:"credentialTwo"`
	Enabled       *bool  `json:"enabled"`
}
