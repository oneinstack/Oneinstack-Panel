package config

// Translation controls the optional response translation fallback. Provider
// credentials are owned by Center and never belong in the Panel config.
type Translation struct {
	Enabled                bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	Mode                   string `mapstructure:"mode" json:"mode" yaml:"mode"`
	Provider               string `mapstructure:"provider" json:"provider" yaml:"provider"`
	CenterURL              string `mapstructure:"centerUrl" json:"centerUrl" yaml:"centerUrl"`
	IdentityPath           string `mapstructure:"identityPath" json:"-" yaml:"identityPath"`
	ActivationCodeFile     string `mapstructure:"activationCodeFile" json:"-" yaml:"activationCodeFile"`
	ResponseTimeoutSeconds int    `mapstructure:"responseTimeoutSeconds" json:"responseTimeoutSeconds" yaml:"responseTimeoutSeconds"`
	CacheTTLMinutes        int    `mapstructure:"cacheTTLMinutes" json:"cacheTTLMinutes" yaml:"cacheTTLMinutes"`
	CacheMaxEntries        int    `mapstructure:"cacheMaxEntries" json:"cacheMaxEntries" yaml:"cacheMaxEntries"`
	MaxTextLength          int    `mapstructure:"maxTextLength" json:"maxTextLength" yaml:"maxTextLength"`
	MaxFieldsPerResponse   int    `mapstructure:"maxFieldsPerResponse" json:"maxFieldsPerResponse" yaml:"maxFieldsPerResponse"`
}
