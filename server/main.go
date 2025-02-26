package server

type ServiceConfig struct {
	StartCmd        string   `json:"start_cmd"`
	StopCmd         string   `json:"stop_cmd"`
	RestartCmd      string   `json:"restart_cmd"`
	ReloadCmd       string   `json:"reload_cmd"`
	SystemdTemplate string   `json:"systemd_template"`
	EnvFiles        []string `json:"env_files"`
	Requires        []string `json:"requires"`
}

type InstallConfig struct {
	BasePath        string            `json:"base_path"`
	ConfigParams    []SoftwareParam   `json:"config_params"`
	ServiceConfig   ServiceConfig     `json:"service_config"`
	HealthCheck     HealthCheck       `json:"health_check"`
	EnvVars         map[string]string `json:"env_vars"`
	ConfigTemplates map[string]string `json:"config_templates"`
}

type HealthCheck struct {
	Command  string `json:"command"`
	Interval string `json:"interval"`
	Retries  int    `json:"retries"`
	Timeout  string `json:"timeout"`
}

type SoftwareParam struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"` // string/number/password/path
	Default     interface{} `json:"default"`
	Description string      `json:"description"`
	Required    bool        `json:"required"`
}

type Version struct {
	Version         string            `json:"version"`
	DownloadURL     string            `json:"download_url"`
	Checksum        string            `json:"checksum"`
	ConfigTemplates map[string]string `json:"config_templates"`
	Params          []ConfigParam     `json:"params"`
}

type ConfigParam struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"`
	Default     interface{} `json:"default"`
	Description string      `json:"description"`
	Required    bool        `json:"required"`
}

type Software struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	InstallConfig InstallConfig `json:"install_config"`
	Versions      []Version     `json:"versions"`
}
