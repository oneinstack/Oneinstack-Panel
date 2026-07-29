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
	PanelEntryEnabled    bool     `json:"panelEntryEnabled"`
	PanelEntryPath       string   `json:"panelEntryPath"`
	RotatePanelEntry     bool     `json:"rotatePanelEntry"`
}

type UpdateSystemTitleRequest struct {
	Title string `json:"title"`
}
