package config

type BastionHost struct {
	Enabled               bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	CollectSchedule       string `mapstructure:"collectSchedule" json:"collectSchedule" yaml:"collectSchedule"`
	CollectTimeoutSeconds int    `mapstructure:"collectTimeoutSeconds" json:"collectTimeoutSeconds" yaml:"collectTimeoutSeconds"`
	MaxConcurrentCollects int    `mapstructure:"maxConcurrentCollects" json:"maxConcurrentCollects" yaml:"maxConcurrentCollects"`
	RetentionDays         int    `mapstructure:"retentionDays" json:"retentionDays" yaml:"retentionDays"`
	CleanupSchedule       string `mapstructure:"cleanupSchedule" json:"cleanupSchedule" yaml:"cleanupSchedule"`
}
