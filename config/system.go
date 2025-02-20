package config

type System struct {
	Port   string `mapstructure:"port" json:"port" yaml:"port"`       // 端口值
	Remote string `mapstructure:"remote" json:"remote" yaml:"remote"` // 端口值
}
