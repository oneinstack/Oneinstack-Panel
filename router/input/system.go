package input

type UpdatePortRequest struct {
	Port string `json:"port"`
}

type UpdatePanelNetworkRequest struct {
	BindAddress          string   `json:"bindAddress"`
	HTTPPort             string   `json:"httpPort"`
	HTTPSEnabled         bool     `json:"httpsEnabled"`
	HTTPSPort            string   `json:"httpsPort"`
	HTTPSCertificateFile string   `json:"httpsCertificateFile"`
	HTTPSPrivateKeyFile  string   `json:"httpsPrivateKeyFile"`
	TrustedProxies       []string `json:"trustedProxies"`
}

type UpdateSystemTitleRequest struct {
	Title string `json:"title"`
}
