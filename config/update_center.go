package config

type UpdateCenter struct {
	Enabled               bool              `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	CenterURL             string            `mapstructure:"centerUrl" json:"centerUrl" yaml:"centerUrl"`
	ManifestURL           string            `mapstructure:"manifestUrl" json:"manifestUrl" yaml:"manifestUrl"`
	Channel               string            `mapstructure:"channel" json:"channel" yaml:"channel"`
	RequestTimeoutSeconds int               `mapstructure:"requestTimeoutSeconds" json:"requestTimeoutSeconds" yaml:"requestTimeoutSeconds"`
	MaxPackageBytes       int64             `mapstructure:"maxPackageBytes" json:"maxPackageBytes" yaml:"maxPackageBytes"`
	MaxExpandedBytes      int64             `mapstructure:"maxExpandedBytes" json:"maxExpandedBytes" yaml:"maxExpandedBytes"`
	HealthTimeoutSeconds  int               `mapstructure:"healthTimeoutSeconds" json:"healthTimeoutSeconds" yaml:"healthTimeoutSeconds"`
	BackupRetention       int               `mapstructure:"backupRetention" json:"backupRetention" yaml:"backupRetention"`
	TrustedKeys           map[string]string `mapstructure:"trustedKeys" json:"trustedKeys" yaml:"trustedKeys"`
}
