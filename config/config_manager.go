package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

// ConfigManager 配置管理器
type ConfigManager struct {
	viper    *viper.Viper
	config   *ServerConfig
	mutex    sync.RWMutex
	watchers []func(*ServerConfig)
}

// ServerConfig 服务器配置结构
type ServerConfig struct {
	System   System   `mapstructure:"system" json:"system" yaml:"system"`
	Database Database `mapstructure:"database" json:"database" yaml:"database"`
	Security Security `mapstructure:"security" json:"security" yaml:"security"`
	Logging  Logging  `mapstructure:"logging" json:"logging" yaml:"logging"`
}

// Database 数据库配置
type Database struct {
	Type     string `mapstructure:"type" json:"type" yaml:"type"`
	Host     string `mapstructure:"host" json:"host" yaml:"host"`
	Port     int    `mapstructure:"port" json:"port" yaml:"port"`
	Database string `mapstructure:"database" json:"database" yaml:"database"`
	Username string `mapstructure:"username" json:"username" yaml:"username"`
	Password string `mapstructure:"password" json:"password" yaml:"password"`
	SSLMode  string `mapstructure:"sslMode" json:"sslMode" yaml:"sslMode"`
}

// Security 安全配置
type Security struct {
	JWTSecret          string   `mapstructure:"jwtSecret" json:"jwtSecret" yaml:"jwtSecret"`
	JWTExpirationHours int      `mapstructure:"jwtExpirationHours" json:"jwtExpirationHours" yaml:"jwtExpirationHours"`
	PasswordMinLength  int      `mapstructure:"passwordMinLength" json:"passwordMinLength" yaml:"passwordMinLength"`
	MaxLoginAttempts   int      `mapstructure:"maxLoginAttempts" json:"maxLoginAttempts" yaml:"maxLoginAttempts"`
	SessionTimeout     int      `mapstructure:"sessionTimeout" json:"sessionTimeout" yaml:"sessionTimeout"`
	EnableTwoFactor    bool     `mapstructure:"enableTwoFactor" json:"enableTwoFactor" yaml:"enableTwoFactor"`
	AllowedOrigins     []string `mapstructure:"allowedOrigins" json:"allowedOrigins" yaml:"allowedOrigins"`
}

// Logging 日志配置
type Logging struct {
	Level      string `mapstructure:"level" json:"level" yaml:"level"`
	Format     string `mapstructure:"format" json:"format" yaml:"format"`
	Output     string `mapstructure:"output" json:"output" yaml:"output"`
	MaxSize    int    `mapstructure:"maxSize" json:"maxSize" yaml:"maxSize"`
	MaxBackups int    `mapstructure:"maxBackups" json:"maxBackups" yaml:"maxBackups"`
	MaxAge     int    `mapstructure:"maxAge" json:"maxAge" yaml:"maxAge"`
	Compress   bool   `mapstructure:"compress" json:"compress" yaml:"compress"`
}

// NewConfigManager 创建新的配置管理器
func NewConfigManager(configPath string) (*ConfigManager, error) {
	cm := &ConfigManager{
		viper:    viper.New(),
		watchers: make([]func(*ServerConfig), 0),
	}

	// 设置配置文件路径
	if configPath == "" {
		configPath = getDefaultConfigPath()
	}

	// 确保配置目录存在
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create config directory: %v", err)
	}

	// 设置viper配置
	cm.viper.SetConfigFile(configPath)
	cm.viper.SetConfigType("yaml")

	// 设置环境变量前缀
	cm.viper.SetEnvPrefix("ONEINSTACK")
	cm.viper.AutomaticEnv()

	// 设置默认值
	cm.setDefaults()

	// 尝试读取配置文件
	if err := cm.viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// 配置文件不存在，创建默认配置
			if err := cm.createDefaultConfig(configPath); err != nil {
				return nil, fmt.Errorf("failed to create default config: %v", err)
			}
		} else {
			return nil, fmt.Errorf("failed to read config file: %v", err)
		}
	}

	// 解析配置
	if err := cm.loadConfig(); err != nil {
		return nil, fmt.Errorf("failed to load config: %v", err)
	}

	// 启动配置文件监控
	cm.viper.WatchConfig()
	cm.viper.OnConfigChange(func(e fsnotify.Event) {
		fmt.Printf("Config file changed: %s\n", e.Name)
		if err := cm.loadConfig(); err != nil {
			fmt.Printf("Failed to reload config: %v\n", err)
		} else {
			cm.notifyWatchers()
		}
	})

	return cm, nil
}

// setDefaults 设置默认配置值
func (cm *ConfigManager) setDefaults() {
	// 系统默认配置
	cm.viper.SetDefault("system.port", "8089")
	cm.viper.SetDefault("system.defaultPath", "/data/")
	cm.viper.SetDefault("system.webPath", "/data/wwwroot/")
	cm.viper.SetDefault("system.logPath", "/data/wwwlogs/")
	cm.viper.SetDefault("system.dataPath", "/data/db/")

	// 数据库默认配置
	cm.viper.SetDefault("database.type", "sqlite")
	cm.viper.SetDefault("database.host", "localhost")
	cm.viper.SetDefault("database.port", 3306)
	cm.viper.SetDefault("database.database", "oneinstack.db")

	// 安全默认配置
	cm.viper.SetDefault("security.jwtExpirationHours", 24)
	cm.viper.SetDefault("security.passwordMinLength", 8)
	cm.viper.SetDefault("security.maxLoginAttempts", 5)
	cm.viper.SetDefault("security.sessionTimeout", 30)
	cm.viper.SetDefault("security.enableTwoFactor", false)

	// 日志默认配置
	cm.viper.SetDefault("logging.level", "info")
	cm.viper.SetDefault("logging.format", "json")
	cm.viper.SetDefault("logging.output", "file")
	cm.viper.SetDefault("logging.maxSize", 100)
	cm.viper.SetDefault("logging.maxBackups", 3)
	cm.viper.SetDefault("logging.maxAge", 28)
	cm.viper.SetDefault("logging.compress", true)
}

// loadConfig 加载配置
func (cm *ConfigManager) loadConfig() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	config := &ServerConfig{}
	if err := cm.viper.Unmarshal(config); err != nil {
		return err
	}

	// 验证配置
	if err := cm.validateConfig(config); err != nil {
		return err
	}

	cm.config = config
	return nil
}

// validateConfig 验证配置
func (cm *ConfigManager) validateConfig(config *ServerConfig) error {
	// 验证端口
	if config.System.Port == "" {
		return fmt.Errorf("system.port cannot be empty")
	}

	// 验证JWT密钥
	if config.Security.JWTSecret != "" && len(config.Security.JWTSecret) < 32 {
		return fmt.Errorf("security.jwtSecret must be at least 32 characters long")
	}

	// 验证日志级别
	validLogLevels := []string{"debug", "info", "warn", "error", "fatal", "panic"}
	isValidLevel := false
	for _, level := range validLogLevels {
		if config.Logging.Level == level {
			isValidLevel = true
			break
		}
	}
	if !isValidLevel {
		return fmt.Errorf("invalid logging.level: %s", config.Logging.Level)
	}

	return nil
}

// createDefaultConfig 创建默认配置文件
func (cm *ConfigManager) createDefaultConfig(configPath string) error {
	defaultConfig := `# Oneinstack Panel Configuration
system:
  port: "8089"
  remote: "http://localhost:8189/v1/sys/update"
  defaultPath: "/data/"
  webPath: "/data/wwwroot/"
  logPath: "/data/wwwlogs/"
  dataPath: "/data/db/"
  jwtSecret: ""  # Will be auto-generated if empty

database:
  type: "sqlite"
  host: "localhost"
  port: 3306
  database: "oneinstack.db"
  username: ""
  password: ""
  sslMode: "disable"

security:
  jwtExpirationHours: 24
  passwordMinLength: 8
  maxLoginAttempts: 5
  sessionTimeout: 30
  enableTwoFactor: false
  allowedOrigins:
    - "*"

logging:
  level: "info"
  format: "json"
  output: "file"
  maxSize: 100
  maxBackups: 3
  maxAge: 28
  compress: true
`

	return os.WriteFile(configPath, []byte(defaultConfig), 0644)
}

// GetConfig 获取当前配置
func (cm *ConfigManager) GetConfig() *ServerConfig {
	cm.mutex.RLock()
	defer cm.mutex.RUnlock()
	return cm.config
}

// UpdateConfig 更新配置
func (cm *ConfigManager) UpdateConfig(key string, value interface{}) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.viper.Set(key, value)

	// 保存到文件
	if err := cm.viper.WriteConfig(); err != nil {
		return fmt.Errorf("failed to save config: %v", err)
	}

	// 重新加载配置
	if err := cm.loadConfig(); err != nil {
		return fmt.Errorf("failed to reload config: %v", err)
	}

	cm.notifyWatchers()
	return nil
}

// AddWatcher 添加配置变更监听器
func (cm *ConfigManager) AddWatcher(watcher func(*ServerConfig)) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	cm.watchers = append(cm.watchers, watcher)
}

// notifyWatchers 通知所有监听器
func (cm *ConfigManager) notifyWatchers() {
	for _, watcher := range cm.watchers {
		go watcher(cm.config)
	}
}

// getDefaultConfigPath 获取默认配置文件路径
func getDefaultConfigPath() string {
	if configPath := os.Getenv("ONEINSTACK_CONFIG_PATH"); configPath != "" {
		return configPath
	}

	// 尝试不同的配置路径
	possiblePaths := []string{
		"/usr/local/one/config.yaml",
		"/etc/oneinstack/config.yaml",
		"./config.yaml",
	}

	for _, path := range possiblePaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// 默认返回第一个路径
	return possiblePaths[0]
}

// GetString 获取字符串配置
func (cm *ConfigManager) GetString(key string) string {
	return cm.viper.GetString(key)
}

// GetInt 获取整数配置
func (cm *ConfigManager) GetInt(key string) int {
	return cm.viper.GetInt(key)
}

// GetBool 获取布尔配置
func (cm *ConfigManager) GetBool(key string) bool {
	return cm.viper.GetBool(key)
}

// GetStringSlice 获取字符串数组配置
func (cm *ConfigManager) GetStringSlice(key string) []string {
	return cm.viper.GetStringSlice(key)
}

// IsProduction 判断是否为生产环境
func (cm *ConfigManager) IsProduction() bool {
	return os.Getenv("GO_ENV") == "production"
}

// IsDevelopment 判断是否为开发环境
func (cm *ConfigManager) IsDevelopment() bool {
	return os.Getenv("GO_ENV") == "development"
}
