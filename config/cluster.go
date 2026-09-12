package config

// ClusterAgent configures an optional managed-node agent. When enabled, this
// Panel registers itself with a user's主控 Panel and periodically publishes a
// resource heartbeat. It does not change Center's software-catalog role.
type ClusterAgent struct {
	Enabled           bool   `mapstructure:"enabled" json:"enabled" yaml:"enabled"`
	ControllerURL     string `mapstructure:"controllerUrl" json:"controllerUrl" yaml:"controllerUrl"`
	Token             string `mapstructure:"token" json:"-" yaml:"token"`
	IntervalSeconds   int    `mapstructure:"intervalSeconds" json:"intervalSeconds" yaml:"intervalSeconds"`
	RequestTimeoutSec int    `mapstructure:"requestTimeoutSeconds" json:"requestTimeoutSeconds" yaml:"requestTimeoutSeconds"`
}
