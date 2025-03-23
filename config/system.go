package config

type System struct {
	Port        string `mapstructure:"port" json:"port" yaml:"port"`                      // 端口值
	Remote      string `mapstructure:"remote" json:"remote" yaml:"remote"`                // 端口值
	DefaultPath string `mapstructure:"defaultPath" json:"defaultPath" yaml:"defaultPath"` // 默认路径
	WebPath     string `mapstructure:"webPath" json:"webPath" yaml:"webPath"`             // web路径
	LogPath     string `mapstructure:"logPath" json:"logPath" yaml:"logPath"`             // 日志路径
	DataPath    string `mapstructure:"dataPath" json:"dataPath" yaml:"dataPath"`          // 数据路径
}
