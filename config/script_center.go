package config

type ScriptCenter struct {
	Enabled               bool              `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	AllowInsecureHTTP     bool              `mapstructure:"allowInsecureHTTP" json:"allowInsecureHTTP" yaml:"allowInsecureHTTP"`
	URL                   string            `mapstructure:"url" json:"url" yaml:"url"`
	Channel               string            `mapstructure:"channel" json:"channel" yaml:"channel"`
	RequestTimeoutSeconds int               `mapstructure:"requestTimeoutSeconds" json:"requestTimeoutSeconds" yaml:"requestTimeoutSeconds"`
	MaxPackageBytes       int64             `mapstructure:"maxPackageBytes" json:"maxPackageBytes" yaml:"maxPackageBytes"`
	MaxExpandedBytes      int64             `mapstructure:"maxExpandedBytes" json:"maxExpandedBytes" yaml:"maxExpandedBytes"`
	CachePath             string            `mapstructure:"cachePath" json:"cachePath" yaml:"cachePath"`
	BundledPath           string            `mapstructure:"bundledPath" json:"bundledPath" yaml:"bundledPath"`
	TrustedKeys           map[string]string `mapstructure:"trustedKeys" json:"trustedKeys" yaml:"trustedKeys"`
}
