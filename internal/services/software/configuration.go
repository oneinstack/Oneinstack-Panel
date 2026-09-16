package software

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	storageService "oneinstack/internal/services/storage"
	"oneinstack/router/input"

	"gorm.io/gorm"
)

const maxConfigurationProbeBytes = 64 * 1024

var (
	ErrConfigurationConflict = errors.New("configuration revision conflict")
	configurationKeyPattern  = regexp.MustCompile(`^[a-z][A-Za-z0-9-]{0,63}$`)
	configurationHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	redisUsernamePattern     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	mongodbUsernamePattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9._-]{0,63}$`)
	mongodbHostnamePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	systemAccountPattern     = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
)

type ConfigurationField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Default     string   `json:"default,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Description string   `json:"description,omitempty"`
	Min         *int     `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ComponentConfiguration struct {
	Component         string                      `json:"component"`
	SoftwareKey       string                      `json:"softwareKey"`
	DisplayName       string                      `json:"displayName"`
	Revision          string                      `json:"revision"`
	ApplyMode         string                      `json:"applyMode"`
	Fields            []ConfigurationField        `json:"fields"`
	Values            map[string]string           `json:"values"`
	PackageSource     string                      `json:"packageSource"`
	InstallParameters []ComponentInstallParameter `json:"installParameters,omitempty"`
	Connection        *ComponentConnection        `json:"connection,omitempty"`
	Runtime           *ComponentRuntime           `json:"runtime,omitempty"`
}

// ComponentInstallParameter exposes the non-secret effective installation
// parameters alongside the managed runtime configuration. Installation
// parameters are read-only here; configuration apply continues to accept only
// the fields declared by the component configuration schema.
type ComponentInstallParameter struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Type        string `json:"type"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
	Default     string `json:"default,omitempty"`
	Value       string `json:"value,omitempty"`
	Description string `json:"description,omitempty"`
}

// ComponentConnection contains the current non-secret Redis connection
// settings. PasswordConfigured is a status flag only; the password itself is
// never returned by a configuration-read endpoint.
type ComponentConnection struct {
	Port               string `json:"port,omitempty"`
	BindAddress        string `json:"bindAddress,omitempty"`
	Username           string `json:"username,omitempty"`
	PasswordConfigured *bool  `json:"passwordConfigured,omitempty"`
}

type ComponentRuntime struct {
	Port        string `json:"port"`
	BindAddress string `json:"bindAddress"`
	SocketPath  string `json:"socketPath"`
	InstallDir  string `json:"installDir"`
	DataDir     string `json:"dataDir"`
	LogDir      string `json:"logDir"`
	RunUser     string `json:"runUser"`
	RunGroup    string `json:"runGroup"`
	ConfigFile  string `json:"configFile,omitempty"`
	VhostDir    string `json:"vhostDir,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	Version     string `json:"version,omitempty"`
	// RuntimeVersion is the explicit read-only runtime identity used by
	// production database components. Version remains for compatibility with
	// existing managed-configuration consumers.
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
}

type ConfigurationChange struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Before string `json:"before"`
	After  string `json:"after"`
	Unit   string `json:"unit,omitempty"`
}

type ConfigurationPreview struct {
	Component  string                `json:"component"`
	Revision   string                `json:"revision"`
	ApplyMode  string                `json:"applyMode"`
	Values     map[string]string     `json:"values"`
	Changes    []ConfigurationChange `json:"changes"`
	HasChanges bool                  `json:"hasChanges"`
}

type configurationDefinition struct {
	Component   string
	SoftwareKey string
	DisplayName string
	ApplyMode   string
	Fields      []ConfigurationField
	Environment map[string]string
}

type configurationProbeCall struct {
	done          chan struct{}
	configuration ComponentConfiguration
	err           error
}

var firewalldConfigurationCalls = struct {
	sync.Mutex
	active map[string]*configurationProbeCall
}{
	active: make(map[string]*configurationProbeCall),
}

// SupportsManagedConfiguration reports whether the component has a complete
// managed-configuration definition and can be exposed to the service UI.
func SupportsManagedConfiguration(component string) bool {
	_, err := componentConfigurationDefinition(component)
	return err == nil
}

func integerField(key, label, unit, description string, min, max int) ConfigurationField {
	minimum, maximum := min, max
	return ConfigurationField{
		Key:         key,
		Label:       label,
		Type:        "integer",
		Unit:        unit,
		Description: description,
		Min:         &minimum,
		Max:         &maximum,
	}
}

func intPointer(value int) *int {
	return &value
}

func componentConfigurationDefinition(component string) (configurationDefinition, error) {
	definition, err := NormalizeServiceComponent(component)
	if err != nil {
		return configurationDefinition{}, err
	}
	result := configurationDefinition{
		Component:   definition.Component,
		SoftwareKey: definition.SoftwareKey,
		DisplayName: definition.DisplayName,
		Environment: make(map[string]string),
	}
	switch definition.Component {
	case "nginx":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{
				Key:         "workerProcesses",
				Label:       "工作进程数",
				Type:        "worker_processes",
				Default:     "auto",
				Description: "建议保持 auto；手动设置范围为 1–99。",
			},
			{Key: "workerConnections", Label: "单进程连接数", Type: "integer", Default: "4096", Min: intPointer(512), Max: intPointer(65535)},
			{Key: "keepaliveTimeout", Label: "长连接超时", Type: "integer", Unit: "秒", Default: "65", Min: intPointer(5), Max: intPointer(300)},
			{Key: "clientMaxBodySize", Label: "请求体上限", Type: "integer", Unit: "MB", Default: "1", Min: intPointer(1), Max: intPointer(10240)},
		}
		result.Environment = map[string]string{
			"workerProcesses":   "ONEINSTACK_CONFIG_WORKER_PROCESSES",
			"workerConnections": "ONEINSTACK_CONFIG_WORKER_CONNECTIONS",
			"keepaliveTimeout":  "ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT",
			"clientMaxBodySize": "ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE",
		}
	case "openresty":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{
				Key:         "workerProcesses",
				Label:       "工作进程数",
				Type:        "worker_processes",
				Default:     "auto",
				Description: "建议保持 auto；手动设置范围为 1–99。",
			},
			{Key: "workerConnections", Label: "单进程连接数", Type: "integer", Default: "4096", Min: intPointer(512), Max: intPointer(65535)},
			{Key: "keepaliveTimeout", Label: "长连接超时", Type: "integer", Unit: "秒", Default: "65", Min: intPointer(5), Max: intPointer(300)},
			{Key: "clientMaxBodySize", Label: "请求体上限", Type: "integer", Unit: "MB", Default: "1", Min: intPointer(1), Max: intPointer(10240)},
			{Key: "openrestyPort", Label: "监听端口", Type: "port", Default: "80", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "phpFpmSocket", Label: "PHP-FPM Socket", Type: "path", Default: "/dev/shm/php-cgi.sock"},
			{Key: "installDir", Label: "安装目录", Type: "path", Default: "/usr/local/openresty", Description: "安装目录不可在线迁移。"},
			{Key: "webRoot", Label: "网站根目录", Type: "path", Default: "/data/wwwroot"},
			{Key: "logDir", Label: "日志目录", Type: "path", Default: "/data/wwwlogs"},
			{Key: "runUser", Label: "运行账号", Type: "string", Default: "www"},
			{Key: "runGroup", Label: "运行用户组", Type: "string", Default: "www"},
		}
		result.Environment = map[string]string{
			"workerProcesses":   "ONEINSTACK_CONFIG_WORKER_PROCESSES",
			"workerConnections": "ONEINSTACK_CONFIG_WORKER_CONNECTIONS",
			"keepaliveTimeout":  "ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT",
			"clientMaxBodySize": "ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE",
			"openrestyPort":     "ONEINSTACK_CONFIG_OPENRESTY_PORT",
			"phpFpmSocket":      "ONEINSTACK_CONFIG_PHP_FPM_SOCKET",
			"installDir":        "ONEINSTACK_CONFIG_INSTALL_DIR",
			"webRoot":           "ONEINSTACK_CONFIG_WEB_ROOT",
			"logDir":            "ONEINSTACK_CONFIG_LOG_DIR",
			"runUser":           "ONEINSTACK_CONFIG_RUN_USER",
			"runGroup":          "ONEINSTACK_CONFIG_RUN_GROUP",
		}
	case "tengine":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{
				Key:         "workerProcesses",
				Label:       "工作进程数",
				Type:        "worker_processes",
				Default:     "auto",
				Description: "建议保持 auto；手动设置范围为 1–99。",
			},
			{Key: "workerConnections", Label: "单进程连接数", Type: "integer", Min: intPointer(512), Max: intPointer(65535)},
			{Key: "keepaliveTimeout", Label: "长连接超时", Type: "integer", Unit: "秒", Min: intPointer(5), Max: intPointer(300)},
			{Key: "clientMaxBodySize", Label: "请求体上限", Type: "integer", Unit: "MB", Min: intPointer(1), Max: intPointer(10240)},
			{Key: "tenginePort", Label: "监听端口", Type: "port", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "phpFpmSocket", Label: "PHP-FPM Socket", Type: "path"},
			{Key: "installDir", Label: "安装目录", Type: "path", Description: "安装目录不可在线迁移。"},
			{Key: "webRoot", Label: "网站根目录", Type: "path"},
			{Key: "logDir", Label: "日志目录", Type: "path"},
			{Key: "runUser", Label: "运行账号", Type: "string"},
			{Key: "runGroup", Label: "运行用户组", Type: "string"},
		}
		result.Environment = map[string]string{
			"workerProcesses":   "ONEINSTACK_CONFIG_WORKER_PROCESSES",
			"workerConnections": "ONEINSTACK_CONFIG_WORKER_CONNECTIONS",
			"keepaliveTimeout":  "ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT",
			"clientMaxBodySize": "ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE",
			"tenginePort":       "ONEINSTACK_CONFIG_TENGINE_PORT",
			"phpFpmSocket":      "ONEINSTACK_CONFIG_PHP_FPM_SOCKET",
			"installDir":        "ONEINSTACK_CONFIG_INSTALL_DIR",
			"webRoot":           "ONEINSTACK_CONFIG_WEB_ROOT",
			"logDir":            "ONEINSTACK_CONFIG_LOG_DIR",
			"runUser":           "ONEINSTACK_CONFIG_RUN_USER",
			"runGroup":          "ONEINSTACK_CONFIG_RUN_GROUP",
		}
	case "caddy":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{Key: "port", Label: "监听端口", Type: "port", Default: "80", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "phpFpmSocket", Label: "PHP-FPM Socket", Type: "path", Default: "/dev/shm/php-cgi.sock"},
			{Key: "webRoot", Label: "网站根目录", Type: "path", Default: "/data/wwwroot"},
			{Key: "logDir", Label: "日志目录", Type: "path", Default: "/data/wwwlogs"},
		}
		result.Environment = map[string]string{
			"port":         "ONEINSTACK_CONFIG_PORT",
			"phpFpmSocket": "ONEINSTACK_CONFIG_PHP_FPM_SOCKET",
			"webRoot":      "ONEINSTACK_CONFIG_WEB_ROOT",
			"logDir":       "ONEINSTACK_CONFIG_LOG_DIR",
		}
	case "apache":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			{Key: "port", Label: "监听端口", Type: "port", Default: "80", Min: intPointer(1), Max: intPointer(65535), Description: "Apache 默认站点监听的 HTTP 端口。"},
			{Key: "maxRequestWorkers", Label: "最大请求工作进程数", Type: "integer", Default: "256", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "keepaliveTimeout", Label: "长连接超时", Type: "integer", Unit: "秒", Default: "5", Min: intPointer(1), Max: intPointer(600)},
			{Key: "phpFpmSocket", Label: "PHP-FPM Socket", Type: "path", Default: "/dev/shm/php-cgi.sock"},
			{Key: "webRoot", Label: "网站根目录", Type: "path", Default: "/data/wwwroot"},
			{Key: "logDir", Label: "日志目录", Type: "path", Default: "/data/wwwlogs"},
		}
		result.Environment = map[string]string{
			"port":              "ONEINSTACK_CONFIG_PORT",
			"maxRequestWorkers": "ONEINSTACK_CONFIG_MAX_REQUEST_WORKERS",
			"keepaliveTimeout":  "ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT",
			"phpFpmSocket":      "ONEINSTACK_CONFIG_PHP_FPM_SOCKET",
			"webRoot":           "ONEINSTACK_CONFIG_WEB_ROOT",
			"logDir":            "ONEINSTACK_CONFIG_LOG_DIR",
		}
	case "mysql":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			{Key: "maxConnections", Label: "最大连接数", Type: "integer", Default: "300", Min: intPointer(10), Max: intPointer(100000)},
			{Key: "maxAllowedPacket", Label: "数据包上限", Type: "integer", Unit: "MB", Default: "64", Min: intPointer(1), Max: intPointer(1024)},
			{Key: "innodbBufferPoolSize", Label: "InnoDB 缓冲池", Type: "integer", Unit: "MB", Default: "128", Min: intPointer(128), Max: intPointer(1048576)},
			{Key: "slowQueryLog", Label: "慢查询日志", Type: "boolean", Default: "false", Description: "记录执行时间超过阈值的 SQL。"},
			{Key: "longQueryTime", Label: "慢查询阈值", Type: "integer", Unit: "秒", Default: "10", Min: intPointer(1), Max: intPointer(600)},
		}
		result.Environment = map[string]string{
			"maxConnections":       "ONEINSTACK_CONFIG_MAX_CONNECTIONS",
			"maxAllowedPacket":     "ONEINSTACK_CONFIG_MAX_ALLOWED_PACKET",
			"innodbBufferPoolSize": "ONEINSTACK_CONFIG_INNODB_BUFFER_POOL_SIZE",
			"slowQueryLog":         "ONEINSTACK_CONFIG_SLOW_QUERY_LOG",
			"longQueryTime":        "ONEINSTACK_CONFIG_LONG_QUERY_TIME",
		}
	case "mariadb":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			{Key: "maxConnections", Label: "最大连接数", Type: "integer", Default: "300", Min: intPointer(10), Max: intPointer(100000)},
			{Key: "maxAllowedPacket", Label: "数据包上限", Type: "integer", Unit: "MB", Default: "64", Min: intPointer(1), Max: intPointer(1024)},
			{Key: "innodbBufferPoolSize", Label: "InnoDB 缓冲池", Type: "integer", Unit: "MB", Default: "128", Min: intPointer(128), Max: intPointer(1048576)},
			{Key: "slowQueryLog", Label: "慢查询日志", Type: "boolean", Default: "false", Description: "记录执行时间超过阈值的 SQL。"},
			{Key: "longQueryTime", Label: "慢查询阈值", Type: "integer", Unit: "秒", Default: "10", Min: intPointer(1), Max: intPointer(600)},
			{Key: "mariadbPort", Label: "监听端口", Type: "port", Default: "3306", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "bindAddress", Label: "绑定地址", Type: "string", Default: "127.0.0.1"},
		}
		result.Environment = map[string]string{
			"maxConnections":       "ONEINSTACK_CONFIG_MAX_CONNECTIONS",
			"maxAllowedPacket":     "ONEINSTACK_CONFIG_MAX_ALLOWED_PACKET",
			"innodbBufferPoolSize": "ONEINSTACK_CONFIG_INNODB_BUFFER_POOL_SIZE",
			"slowQueryLog":         "ONEINSTACK_CONFIG_SLOW_QUERY_LOG",
			"longQueryTime":        "ONEINSTACK_CONFIG_LONG_QUERY_TIME",
			"mariadbPort":          "ONEINSTACK_CONFIG_MARIADB_PORT",
			"bindAddress":          "ONEINSTACK_CONFIG_BIND_ADDRESS",
		}
	case "mongodb":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			{Key: "mongodbPort", Label: "监听端口", Type: "port", Default: "27017", Min: intPointer(1), Max: intPointer(65535)},
			{Key: "bindIp", Label: "绑定地址", Type: "string", Default: "127.0.0.1", Description: "逗号分隔的 IP 或主机名；认证始终启用。"},
			{Key: "maxIncomingConnections", Label: "最大入站连接数", Type: "integer", Default: "0", Min: intPointer(0), Max: intPointer(1000000), Description: "0 表示自动；非零值最小为 100。"},
			{Key: "wiredTigerCacheSizeGB", Label: "WiredTiger 缓存", Type: "integer", Default: "0", Unit: "GB", Min: intPointer(0), Max: intPointer(1024), Description: "0 表示自动。"},
			{Key: "operationProfilingMode", Label: "性能分析模式", Type: "select", Default: "off", Options: []string{"off", "slowOp", "all"}},
			{Key: "slowOpThresholdMs", Label: "慢操作阈值", Type: "integer", Unit: "毫秒", Default: "100", Min: intPointer(1), Max: intPointer(600000)},
		}
		result.Environment = map[string]string{
			"mongodbPort":            "ONEINSTACK_CONFIG_MONGODB_PORT",
			"bindIp":                 "ONEINSTACK_CONFIG_BIND_IP",
			"maxIncomingConnections": "ONEINSTACK_CONFIG_MAX_INCOMING_CONNECTIONS",
			"wiredTigerCacheSizeGB":  "ONEINSTACK_CONFIG_WIREDTIGER_CACHE_SIZE_GB",
			"operationProfilingMode": "ONEINSTACK_CONFIG_OPERATION_PROFILING_MODE",
			"slowOpThresholdMs":      "ONEINSTACK_CONFIG_SLOW_OP_THRESHOLD_MS",
		}
	case "php":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{Key: "memoryLimit", Label: "脚本内存上限", Type: "integer", Unit: "MB", Default: "256", Min: intPointer(32), Max: intPointer(8192)},
			{Key: "uploadMaxFilesize", Label: "单文件上传上限", Type: "integer", Unit: "MB", Default: "2", Min: intPointer(1), Max: intPointer(2048)},
			{Key: "postMaxSize", Label: "POST 数据上限", Type: "integer", Unit: "MB", Default: "8", Min: intPointer(1), Max: intPointer(4096)},
			{Key: "maxExecutionTime", Label: "脚本执行超时", Type: "integer", Unit: "秒", Default: "30", Min: intPointer(10), Max: intPointer(3600)},
			{Key: "pmMaxChildren", Label: "最大子进程数", Type: "integer", Default: "32", Min: intPointer(1), Max: intPointer(10000)},
			{Key: "pmStartServers", Label: "启动进程数", Type: "integer", Default: "4", Min: intPointer(1), Max: intPointer(10000)},
			{Key: "pmMinSpareServers", Label: "最小空闲进程", Type: "integer", Default: "2", Min: intPointer(1), Max: intPointer(10000)},
			{Key: "pmMaxSpareServers", Label: "最大空闲进程", Type: "integer", Default: "8", Min: intPointer(1), Max: intPointer(10000)},
		}
		result.Environment = map[string]string{
			"memoryLimit":       "ONEINSTACK_CONFIG_MEMORY_LIMIT",
			"uploadMaxFilesize": "ONEINSTACK_CONFIG_UPLOAD_MAX_FILESIZE",
			"postMaxSize":       "ONEINSTACK_CONFIG_POST_MAX_SIZE",
			"maxExecutionTime":  "ONEINSTACK_CONFIG_MAX_EXECUTION_TIME",
			"pmMaxChildren":     "ONEINSTACK_CONFIG_PM_MAX_CHILDREN",
			"pmStartServers":    "ONEINSTACK_CONFIG_PM_START_SERVERS",
			"pmMinSpareServers": "ONEINSTACK_CONFIG_PM_MIN_SPARE_SERVERS",
			"pmMaxSpareServers": "ONEINSTACK_CONFIG_PM_MAX_SPARE_SERVERS",
		}
	case "redis":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			{Key: "port", Label: "监听端口", Type: "port", Default: "6379", Min: intPointer(1), Max: intPointer(65535), Description: "Redis TCP 监听端口。"},
			{Key: "maxmemory", Label: "最大内存", Type: "integer", Unit: "MB", Default: "0", Min: intPointer(0), Max: intPointer(1048576)},
			{
				Key:         "maxmemoryPolicy",
				Label:       "内存淘汰策略",
				Type:        "select",
				Default:     "noeviction",
				Description: "达到内存上限后 Redis 处理新写入的方式。",
				Options: []string{
					"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random",
					"volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl",
				},
			},
			{Key: "appendonly", Label: "AOF 持久化", Type: "boolean", Default: "true", Description: "将写操作追加到 AOF 文件。"},
			{Key: "timeout", Label: "空闲连接超时", Type: "integer", Unit: "秒", Default: "0", Min: intPointer(0), Max: intPointer(86400)},
			{Key: "tcpKeepalive", Label: "TCP Keepalive", Type: "integer", Unit: "秒", Default: "300", Min: intPointer(0), Max: intPointer(3600)},
		}
		result.Environment = map[string]string{
			"port":            "ONEINSTACK_CONFIG_PORT",
			"maxmemory":       "ONEINSTACK_CONFIG_MAXMEMORY",
			"maxmemoryPolicy": "ONEINSTACK_CONFIG_MAXMEMORY_POLICY",
			"appendonly":      "ONEINSTACK_CONFIG_APPENDONLY",
			"timeout":         "ONEINSTACK_CONFIG_TIMEOUT",
			"tcpKeepalive":    "ONEINSTACK_CONFIG_TCP_KEEPALIVE",
		}
	case "fail2ban":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{Key: "maxRetry", Label: "最大重试次数", Type: "integer", Default: "5", Min: intPointer(1), Max: intPointer(100)},
			{Key: "findTimeSeconds", Label: "统计窗口", Type: "integer", Unit: "秒", Default: "600", Min: intPointer(1), Max: intPointer(604800)},
			{Key: "banTimeSeconds", Label: "封禁时长", Type: "integer", Unit: "秒", Default: "3600", Min: intPointer(60), Max: intPointer(31536000)},
			{Key: "ignoreIps", Label: "忽略 IP", Type: "string", Default: "127.0.0.1/8 ::1", Description: "空格分隔的 IP 地址或网段，这些地址不会被 Fail2ban 封禁。"},
		}
		result.Environment = map[string]string{
			"maxRetry":        "ONEINSTACK_CONFIG_MAX_RETRY",
			"findTimeSeconds": "ONEINSTACK_CONFIG_FIND_TIME",
			"banTimeSeconds":  "ONEINSTACK_CONFIG_BAN_TIME",
			"ignoreIps":       "ONEINSTACK_CONFIG_IGNORE_IPS",
		}
	case "firewalld":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			{Key: "default-zone", Label: "默认区域", Type: "string", Default: "public"},
			{Key: "log-denied", Label: "拒绝日志", Type: "select", Default: "off", Options: []string{"off", "unicast", "broadcast", "multicast", "all"}},
			{Key: "rules", Label: "受管规则", Type: "json", Default: `{"managed":{"zones":[],"directRules":[],"icmpBlocks":[],"forwardPorts":[]},"effective":{"defaultZone":"public","logDenied":"off","zones":[],"directRules":[]}}`},
		}
		result.Environment = map[string]string{
			"default-zone": "ONEINSTACK_CONFIG_DEFAULT_ZONE",
			"log-denied":   "ONEINSTACK_CONFIG_LOG_DENIED",
			"rules":        "ONEINSTACK_CONFIG_RULES",
		}
	default:
		return configurationDefinition{}, fmt.Errorf("component %s does not support managed configuration", component)
	}
	return result, nil
}

func manifestConfigurationDefinition(
	base configurationDefinition,
	manifest scriptregistry.Manifest,
) (configurationDefinition, error) {
	if len(manifest.Configuration.Fields) == 0 {
		return base, nil
	}
	result := base
	result.ApplyMode = manifest.Configuration.ApplyMode
	result.Fields = make([]ConfigurationField, 0, len(manifest.Configuration.Fields))
	result.Environment = make(map[string]string, len(manifest.Configuration.Fields))
	for _, field := range manifest.Configuration.Fields {
		if strings.TrimSpace(field.Env) == "" {
			return configurationDefinition{}, fmt.Errorf("configuration field %s has no action environment parameter", field.Key)
		}
		result.Fields = append(result.Fields, ConfigurationField{
			Key:         field.Key,
			Label:       field.Label,
			Type:        field.Type,
			Default:     field.Default,
			Unit:        field.Unit,
			Description: field.Description,
			Min:         field.Min,
			Max:         field.Max,
			Options:     append([]string(nil), field.Options...),
		})
		result.Environment[field.Key] = field.Env
	}
	return result, nil
}

func NormalizeConfigurationValues(component string, values map[string]string) (map[string]string, error) {
	definition, err := componentConfigurationDefinition(component)
	if err != nil {
		return nil, err
	}
	return normalizeConfigurationValues(definition, values)
}

func normalizeConfigurationValues(definition configurationDefinition, values map[string]string) (map[string]string, error) {
	// Fields with a declared default may be omitted or submitted as an empty
	// value. Unknown fields remain rejected so callers cannot bypass the managed
	// configuration schema.
	knownFields := make(map[string]struct{}, len(definition.Fields))
	for _, field := range definition.Fields {
		knownFields[field.Key] = struct{}{}
	}
	for key := range values {
		if _, exists := knownFields[key]; !exists {
			return nil, errors.New("configuration must contain every managed field and no unknown fields")
		}
	}
	result := make(map[string]string, len(definition.Fields))
	for _, field := range definition.Fields {
		if definition.Component == "firewalld" {
			if _, exists := values[field.Key]; !exists {
				return nil, fmt.Errorf("configuration field %s is required and must be returned by configGet", field.Key)
			}
		}
		value := strings.TrimSpace(values[field.Key])
		if value == "" && strings.TrimSpace(field.Default) != "" {
			value = strings.TrimSpace(field.Default)
		}
		if value == "" {
			return nil, fmt.Errorf("configuration field %s is required", field.Key)
		}
		if strings.ContainsAny(value, "\x00\r\n") || (field.Type != "json" && len(value) > 128) || (field.Type == "json" && len(value) > 512<<10) {
			return nil, fmt.Errorf("configuration field %s contains invalid data", field.Key)
		}
		switch field.Type {
		case "integer":
			number, parseErr := strconv.Atoi(value)
			if parseErr != nil || field.Min == nil || field.Max == nil ||
				number < *field.Min || number > *field.Max {
				return nil, fmt.Errorf("configuration field %s is outside the allowed range", field.Key)
			}
			value = strconv.Itoa(number)
		case "boolean":
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("configuration field %s must be true or false", field.Key)
			}
		case "select":
			if !containsConfigurationOption(field.Options, value) {
				return nil, fmt.Errorf("configuration field %s has an unsupported value", field.Key)
			}
		case "port":
			number, parseErr := strconv.Atoi(value)
			minimum, maximum := 1, 65535
			if field.Min != nil {
				minimum = *field.Min
			}
			if field.Max != nil {
				maximum = *field.Max
			}
			if parseErr != nil || number < minimum || number > maximum {
				return nil, fmt.Errorf("configuration field %s is outside the allowed range", field.Key)
			}
			value = strconv.Itoa(number)
		case "path":
			if value == "" || !strings.HasPrefix(value, "/") || filepath.Clean(value) != value {
				return nil, fmt.Errorf("configuration field %s must be a normalized absolute path", field.Key)
			}
			switch value {
			case "/", "/usr", "/usr/local", "/etc", "/var", "/data", "/home", "/root":
				return nil, fmt.Errorf("configuration field %s is too broad", field.Key)
			}
		case "string":
			if value == "" {
				return nil, fmt.Errorf("configuration field %s is required", field.Key)
			}
		case "worker_processes":
			if value != "auto" {
				number, parseErr := strconv.Atoi(value)
				if parseErr != nil || number < 1 || number > 99 {
					return nil, errors.New("workerProcesses must be auto or an integer from 1 to 99")
				}
				value = strconv.Itoa(number)
			}
		case "json":
			var decoded any
			decoder := json.NewDecoder(strings.NewReader(value))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err != nil || decoded == nil {
				return nil, fmt.Errorf("configuration field %s must contain valid JSON", field.Key)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return nil, fmt.Errorf("configuration field %s must contain one JSON value", field.Key)
			}
			encoded, err := json.Marshal(decoded)
			if err != nil {
				return nil, fmt.Errorf("configuration field %s must contain valid JSON", field.Key)
			}
			value = string(encoded)
		default:
			return nil, fmt.Errorf("configuration field %s has an unsupported type", field.Key)
		}
		result[field.Key] = value
	}
	if definition.Component == "php" {
		upload, _ := strconv.Atoi(result["uploadMaxFilesize"])
		post, _ := strconv.Atoi(result["postMaxSize"])
		children, _ := strconv.Atoi(result["pmMaxChildren"])
		start, _ := strconv.Atoi(result["pmStartServers"])
		minimum, _ := strconv.Atoi(result["pmMinSpareServers"])
		maximum, _ := strconv.Atoi(result["pmMaxSpareServers"])
		if post < upload {
			return nil, errors.New("postMaxSize must be greater than or equal to uploadMaxFilesize")
		}
		if minimum > start || start > maximum || maximum > children {
			return nil, errors.New("PHP-FPM process counts must satisfy min spare ≤ start ≤ max spare ≤ max children")
		}
	}
	if definition.Component == "apache" {
		webRoot := strings.TrimSpace(result["webRoot"])
		if webRoot != "/data/wwwroot" && webRoot != "/data/wwwroot/default" {
			return nil, errors.New("Apache webRoot must be /data/wwwroot")
		}
		logDir := strings.TrimSpace(result["logDir"])
		if logDir != "/data/wwwlogs" && !strings.HasPrefix(logDir, "/data/wwwlogs/") {
			return nil, errors.New("Apache logDir must be below /data/wwwlogs")
		}
		for _, key := range []string{"phpFpmSocket", "logDir"} {
			if strings.ContainsAny(result[key], " \t\r\n\"';|&$`*?[]") {
				return nil, fmt.Errorf("Apache configuration field %s contains unsupported path characters", key)
			}
		}
	}
	if definition.Component == "openresty" {
		for _, key := range []string{"phpFpmSocket", "installDir", "webRoot", "logDir"} {
			if strings.ContainsAny(result[key], " \t\r\n\"';|&$`*?[]{}()<>\\#") {
				return nil, fmt.Errorf("OpenResty configuration field %s contains unsupported path characters", key)
			}
		}
		for _, key := range []string{"runUser", "runGroup"} {
			if !systemAccountPattern.MatchString(result[key]) {
				return nil, fmt.Errorf("OpenResty configuration field %s must be a valid system account identifier", key)
			}
		}
	}
	if definition.Component == "mariadb" && net.ParseIP(result["bindAddress"]) == nil {
		return nil, errors.New("bindAddress must be a valid IPv4 or IPv6 address")
	}
	if definition.Component == "mongodb" {
		maxConnections, _ := strconv.Atoi(result["maxIncomingConnections"])
		if maxConnections > 0 && maxConnections < 100 {
			return nil, errors.New("maxIncomingConnections must be 0 or an integer from 100 to 1000000")
		}
		if err := validateMongoDBBindIP(result["bindIp"]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func validateMongoDBBindIP(value string) error {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 32 {
		return errors.New("bindIp must contain 1-32 IP addresses or hostnames")
	}
	for _, raw := range parts {
		part := strings.TrimSpace(raw)
		if part == "" || part != raw || strings.ContainsAny(part, "\r\n\t{}[]#&*!|><'\"\\") {
			return errors.New("bindIp contains an invalid IP address or hostname")
		}
		if net.ParseIP(part) == nil && (!mongodbHostnamePattern.MatchString(part) || strings.Contains(part, "..") || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".")) {
			return errors.New("bindIp contains an invalid IP address or hostname")
		}
	}
	return nil
}

func containsConfigurationOption(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func (installer *Installer) InspectServiceConfiguration(
	ctx context.Context,
	component string,
	version string,
) (ComponentConfiguration, error) {
	if strings.EqualFold(strings.TrimSpace(component), "firewalld") {
		return installer.inspectFirewalldConfiguration(ctx, component, version)
	}
	return installer.inspectServiceConfiguration(ctx, component, version)
}

func (installer *Installer) inspectFirewalldConfiguration(
	ctx context.Context,
	component string,
	version string,
) (ComponentConfiguration, error) {
	key := strings.ToLower(strings.TrimSpace(component)) + "|" + strings.TrimSpace(version)
	firewalldConfigurationCalls.Lock()
	if existing := firewalldConfigurationCalls.active[key]; existing != nil {
		firewalldConfigurationCalls.Unlock()
		select {
		case <-existing.done:
			return cloneComponentConfiguration(existing.configuration), existing.err
		case <-ctx.Done():
			return ComponentConfiguration{}, ctx.Err()
		}
	}
	call := &configurationProbeCall{done: make(chan struct{})}
	firewalldConfigurationCalls.active[key] = call
	firewalldConfigurationCalls.Unlock()

	configuration, err := installer.inspectServiceConfiguration(ctx, component, version)
	firewalldConfigurationCalls.Lock()
	call.configuration = cloneComponentConfiguration(configuration)
	call.err = err
	delete(firewalldConfigurationCalls.active, key)
	close(call.done)
	firewalldConfigurationCalls.Unlock()
	if err != nil {
		return ComponentConfiguration{}, err
	}
	return cloneComponentConfiguration(configuration), nil
}

func (installer *Installer) inspectServiceConfiguration(
	ctx context.Context,
	component string,
	version string,
) (ComponentConfiguration, error) {
	definition, err := componentConfigurationDefinition(component)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	componentPackage, err := installer.resolveConfigurationPackage(definition, version)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	definition, err = manifestConfigurationDefinition(definition, componentPackage.Manifest)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, "configGet")
	if err != nil {
		return ComponentConfiguration{}, err
	}
	params := installedServiceInstallParams(
		definition.SoftwareKey,
		definition.Component,
		strings.TrimSpace(version),
	)
	installer.setScriptParams(scriptInfo, params)
	scriptInfo.Params["ONEINSTACK_CONFIG_OPERATION"] = "get"
	output, err := installer.scriptManager.ExecuteProbe(ctx, scriptInfo, maxConfigurationProbeBytes)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	configuration, err := parseComponentConfiguration(output, definition)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	configuration.PackageSource = componentPackage.Source
	configuration.InstallParameters = componentInstallParameters(definition.Component, componentPackage.Manifest.Parameters, scriptInfo.Params)
	if configuration.Connection == nil && definition.Component == "redis" {
		configuration.Connection = redisConnectionFromParameters(scriptInfo.Params)
	}
	return configuration, nil
}

func cloneComponentConfiguration(configuration ComponentConfiguration) ComponentConfiguration {
	clone := configuration
	clone.Fields = append([]ConfigurationField(nil), configuration.Fields...)
	for index := range clone.Fields {
		clone.Fields[index].Options = append([]string(nil), configuration.Fields[index].Options...)
		if configuration.Fields[index].Min != nil {
			value := *configuration.Fields[index].Min
			clone.Fields[index].Min = &value
		}
		if configuration.Fields[index].Max != nil {
			value := *configuration.Fields[index].Max
			clone.Fields[index].Max = &value
		}
	}
	if configuration.Values != nil {
		clone.Values = make(map[string]string, len(configuration.Values))
		for key, value := range configuration.Values {
			clone.Values[key] = value
		}
	}
	clone.InstallParameters = append([]ComponentInstallParameter(nil), configuration.InstallParameters...)
	if configuration.Connection != nil {
		connection := *configuration.Connection
		if configuration.Connection.PasswordConfigured != nil {
			configured := *configuration.Connection.PasswordConfigured
			connection.PasswordConfigured = &configured
		}
		clone.Connection = &connection
	}
	if configuration.Runtime != nil {
		runtime := *configuration.Runtime
		clone.Runtime = &runtime
	}
	return clone
}

func componentInstallParameters(
	component string,
	parameters []scriptregistry.Parameter,
	values map[string]string,
) []ComponentInstallParameter {
	result := make([]ComponentInstallParameter, 0, len(parameters))
	for _, parameter := range parameters {
		if serverOwnedInstallParameterForComponent(component, parameter.Name) {
			continue
		}
		envName := strings.TrimSpace(parameter.Env)
		if envName == "" {
			envName = strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimSpace(parameter.Name)))
		}
		secret := parameter.Secret || strings.EqualFold(strings.TrimSpace(parameter.Type), "password")
		value := ""
		if !secret {
			value = strings.TrimSpace(values[envName])
		}
		result = append(result, ComponentInstallParameter{
			Key:         parameter.Name,
			Label:       componentInstallParameterLabel(parameter.Name),
			Type:        parameter.Type,
			Required:    parameter.Required,
			Secret:      secret,
			Default:     parameter.Default,
			Value:       value,
			Description: parameter.Description,
		})
	}
	return result
}

func serverOwnedInstallParameterName(name string) bool {
	normalized := strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimSpace(name)))
	switch normalized {
	case "ONEINSTACK_INSTALL_MODE", "ONEINSTACK_OFFLINE_PACKAGE_PATH", "ONEINSTACK_COMPONENT_STATE",
		"INSTALL_MODE", "OFFLINE_PACKAGE_ID", "OFFLINE_PACKAGE_PATH", "COMPONENT_STATE_DIR",
		"UNINSTALL_DATA_POLICY", "UNINSTALL_CONFIRM_DATA_DELETION", "WEB_VHOST_ROOT":
		return true
	default:
		return false
	}
}

func serverOwnedInstallParameterForComponent(component, name string) bool {
	if serverOwnedInstallParameterName(name) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(component), "caddy") {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "INSTALL_DIR", "RUN_USER", "RUN_GROUP":
		return true
	default:
		return false
	}
}

func componentInstallParameterLabel(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SOFTWARE_VERSION":
		return "Software version"
	case "INSTALL_DIR":
		return "Installation directory"
	case "DATA_DIR":
		return "Data directory"
	case "REDIS_PORT":
		return "Redis listener port"
	case "REDIS_BIND":
		return "Redis listener addresses"
	case "REDIS_USERNAME":
		return "Redis login username"
	case "REDIS_PASSWORD":
		return "Redis password"
	case "MONGODB_PORT":
		return "MongoDB listener port"
	case "MONGODB_BIND_IP":
		return "MongoDB listener addresses"
	case "MONGODB_ADMIN_USERNAME":
		return "MongoDB administrator username"
	case "MONGODB_ADMIN_PASSWORD":
		return "MongoDB administrator password"
	case "PORT":
		return "HTTP listener port"
	case "TENGINE_PORT":
		return "Tengine HTTP listener port"
	case "OPENRESTY_PORT":
		return "OpenResty HTTP listener port"
	case "CADDY_PORT":
		return "Caddy HTTP listener port"
	case "PHP_FPM_SOCKET":
		return "PHP-FPM socket"
	case "WEB_ROOT":
		return "Website root directory"
	case "LOG_DIR":
		return "Log directory"
	case "WEB_VHOST_ROOT":
		return "Panel vhost directory"
	case "RUN_USER":
		return "Runtime user"
	case "RUN_GROUP":
		return "Runtime group"
	case "ONEINSTACK_INSTALL_MODE":
		return "Installation mode"
	case "ONEINSTACK_OFFLINE_PACKAGE_PATH":
		return "Offline Bundle path"
	case "UNINSTALL_DATA_POLICY":
		return "Uninstall data policy"
	case "UNINSTALL_CONFIRM_DATA_DELETION":
		return "Confirm data deletion"
	case "ONEINSTACK_COMPONENT_STATE":
		return "Component state directory"
	default:
		return strings.TrimSpace(name)
	}
}

func redisConnectionFromParameters(values map[string]string) *ComponentConnection {
	if len(values) == 0 {
		return nil
	}
	connection := &ComponentConnection{
		Port:        installParameterValue(values, "REDIS_PORT", "redis-port", "redisPort"),
		BindAddress: installParameterValue(values, "REDIS_BIND", "redis-bind", "redisBind"),
		Username:    installParameterValue(values, "REDIS_USERNAME", "redis-username", "redisUsername", "username"),
	}
	if connection.Port == "" && connection.BindAddress == "" && connection.Username == "" {
		return nil
	}
	return connection
}

func (installer *Installer) ApplyServiceConfigurationTask(
	ctx context.Context,
	component string,
	version string,
	revision string,
	values map[string]string,
	logPath string,
	observer script.ExecutionObserver,
) (string, error) {
	definition, err := componentConfigurationDefinition(component)
	if err != nil {
		return "", err
	}
	revision = strings.TrimSpace(revision)
	if !configurationHashPattern.MatchString(revision) {
		return "", errors.New("invalid configuration revision")
	}
	componentPackage, err := installer.resolveConfigurationPackage(definition, version)
	if err != nil {
		return "", err
	}
	definition, err = manifestConfigurationDefinition(definition, componentPackage.Manifest)
	if err != nil {
		return "", err
	}
	normalized, err := normalizeConfigurationValues(definition, values)
	if err != nil {
		return "", err
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, "configApply")
	if err != nil {
		return "", err
	}
	reportPackageResolution(observer, scriptInfo)
	params := installedServiceInstallParams(
		definition.SoftwareKey,
		definition.Component,
		strings.TrimSpace(version),
	)
	if definition.Component == "mysql" || definition.Component == "mariadb" {
		username, password, found, credentialErr := storageService.ManagedLocalMySQLCredential(params.Port)
		if credentialErr != nil {
			return "", fmt.Errorf("load managed database credential: %w", credentialErr)
		}
		if !found {
			return "", errors.New("managed database credential is unavailable")
		}
		params.Pwd = password
		if params.Parameters == nil {
			params.Parameters = make(map[string]string)
		}
		params.Parameters["mysql-username"] = username
	}
	installer.setScriptParams(scriptInfo, params)
	scriptInfo.Params["ONEINSTACK_CONFIG_OPERATION"] = "apply"
	scriptInfo.Params["ONEINSTACK_CONFIG_REVISION"] = revision
	if definition.Component == "firewalld" {
		configFile, err := writeFirewalldConfigurationFile(normalized)
		if err != nil {
			return "", err
		}
		defer os.Remove(configFile)
		scriptInfo.Params["ONEINSTACK_CONFIG_FILE"] = configFile
	} else {
		for key, value := range normalized {
			scriptInfo.Params[definition.Environment[key]] = value
		}
	}
	taskID, err := installer.scriptManager.ExecuteScriptTask(ctx, scriptInfo, params, logPath, observer)
	if err != nil {
		return "", err
	}
	if err := persistManagedConfiguration(params, normalized); err != nil {
		return "", fmt.Errorf("STATE_REPAIR_REQUIRED: persist managed component configuration: %w", err)
	}
	return taskID, nil
}

func writeFirewalldConfigurationFile(values map[string]string) (string, error) {
	payload := make(map[string]json.RawMessage, 3)
	for _, key := range []string{"default-zone", "log-denied"} {
		value, err := json.Marshal(values[key])
		if err != nil {
			return "", fmt.Errorf("encode firewalld field %s: %w", key, err)
		}
		payload[key] = value
	}
	payload["rules"] = json.RawMessage(values["rules"])
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode firewalld configuration: %w", err)
	}
	file, err := os.CreateTemp("", "oneinstack-firewalld-config-*.json")
	if err != nil {
		return "", fmt.Errorf("create firewalld configuration candidate: %w", err)
	}
	name := file.Name()
	removeOnError := true
	defer func() {
		_ = file.Close()
		if removeOnError {
			_ = os.Remove(name)
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return "", fmt.Errorf("secure firewalld configuration candidate: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		return "", fmt.Errorf("write firewalld configuration candidate: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close firewalld configuration candidate: %w", err)
	}
	removeOnError = false
	return name, nil
}

func persistManagedConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "php") {
		return persistManagedPHPConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "apache") {
		return persistManagedApacheConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "openresty") {
		return persistManagedOpenRestyConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "caddy") {
		return persistManagedCaddyConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "firewalld") {
		return persistManagedFirewalldConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "mongodb") {
		return persistManagedMongoDBConfiguration(params, values)
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "mariadb") {
		return persistManagedMariaDBConfiguration(params, values)
	}
	return persistManagedMySQLConfiguration(params, values)
}

func persistManagedMongoDBConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).Where("(`key` = ? OR component = ?)", "mongodb", "mongodb")
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode MongoDB runtime parameters: %w", err)
		}
	}
	if value := strings.TrimSpace(values["mongodbPort"]); value != "" {
		runtime["mongodb-port"] = value
	}
	if value := strings.TrimSpace(values["bindIp"]); value != "" {
		runtime["mongodb-bind-ip"] = value
	}
	for _, key := range []string{"maxIncomingConnections", "wiredTigerCacheSizeGB", "operationProfilingMode", "slowOpThresholdMs"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			runtime[key] = value
		}
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode MongoDB runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["mongodbPort"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("MongoDB software runtime parameters were not updated")
	}
	return nil
}

func persistManagedCaddyConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).
		Where("(`key` = ? OR component = ?)", "caddy", "caddy")
	if strings.TrimSpace(params.Key) != "" {
		query = query.Where("(`key` = ? OR component = ?)", params.Key, "caddy")
	}
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode Caddy runtime parameters: %w", err)
		}
	}
	assign := func(valueKey string, runtimeKeys ...string) {
		value := strings.TrimSpace(values[valueKey])
		if value == "" {
			return
		}
		for _, runtimeKey := range runtimeKeys {
			runtime[runtimeKey] = value
		}
	}
	assign("port", "caddy-port", "port")
	assign("phpFpmSocket", "caddy-php-fpm-socket", "php-fpm-socket")
	assign("webRoot", "caddy-web-root", "web-root")
	assign("logDir", "caddy-log-dir", "log-dir")
	runtime["caddy-install-dir"] = "/usr/local/caddy"
	runtime["install-dir"] = "/usr/local/caddy"
	runtime["caddy-vhost-root"] = "/usr/local/one/vhost"
	runtime["web-vhost-root"] = "/usr/local/one/vhost"
	runtime["caddy-run-user"] = "caddy"
	runtime["run-user"] = "caddy"
	runtime["caddy-run-group"] = "caddy"
	runtime["run-group"] = "caddy"
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode Caddy runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["port"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("Caddy software runtime parameters were not updated")
	}
	return nil
}

func persistManagedOpenRestyConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).
		Where("(`key` = ? OR component = ?)", "openresty", "openresty")
	if strings.TrimSpace(params.Key) != "" {
		query = query.Where("(`key` = ? OR component = ?)", params.Key, "openresty")
	}
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode OpenResty runtime parameters: %w", err)
		}
	}
	assign := func(valueKey string, runtimeKeys ...string) {
		value := strings.TrimSpace(values[valueKey])
		if value == "" {
			return
		}
		for _, runtimeKey := range runtimeKeys {
			runtime[runtimeKey] = value
		}
	}
	assign("openrestyPort", "openresty-port", "port")
	assign("phpFpmSocket", "openresty-php-fpm-socket", "php-fpm-socket")
	assign("installDir", "openresty-install-dir", "install-dir")
	assign("webRoot", "openresty-web-root", "web-root")
	assign("logDir", "openresty-log-dir", "log-dir")
	assign("runUser", "openresty-run-user", "run-user")
	assign("runGroup", "openresty-run-group", "run-group")
	if runtime["openresty-install-dir"] == "" {
		runtime["openresty-install-dir"] = "/usr/local/openresty"
		runtime["install-dir"] = "/usr/local/openresty"
	}
	if runtime["openresty-vhost-root"] == "" {
		runtime["openresty-vhost-root"] = "/usr/local/one/vhost"
		runtime["web-vhost-root"] = "/usr/local/one/vhost"
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode OpenResty runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["openrestyPort"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("OpenResty software runtime parameters were not updated")
	}
	return nil
}

func persistManagedFirewalldConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).
		Where("(`key` = ? OR component = ?)", "firewalld", "firewalld")
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode firewalld runtime parameters: %w", err)
		}
	}
	for key, value := range values {
		runtime[key] = value
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode firewalld runtime parameters: %w", err)
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).
		Updates(map[string]interface{}{"runtime_params": string(encoded)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("firewalld software runtime parameters were not updated")
	}
	return nil
}

func persistManagedPHPConfiguration(params *input.InstallParams, values map[string]string) error {
	var row models.Software
	query := app.DB().Where("installed = ?", true)
	if strings.TrimSpace(params.Key) != "" {
		query = query.Where("(`key` = ? OR component = ?)", params.Key, "php")
	}
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode PHP runtime parameters: %w", err)
		}
	}
	assign := func(valueKey, runtimeKey string) {
		if value := strings.TrimSpace(values[valueKey]); value != "" {
			runtime[runtimeKey] = value
		}
	}
	if value := strings.TrimSpace(values["memoryLimit"]); value != "" {
		runtime["php-memory-limit"] = value + "M"
	}
	assign("uploadMaxFilesize", "php-upload-max-filesize")
	assign("postMaxSize", "php-post-max-size")
	assign("maxExecutionTime", "php-max-execution-time")
	assign("pmMaxChildren", "php-pm-max-children")
	assign("pmStartServers", "php-pm-start-servers")
	assign("pmMinSpareServers", "php-pm-min-spare-servers")
	assign("pmMaxSpareServers", "php-pm-max-spare-servers")
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode PHP runtime parameters: %w", err)
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).
		Updates(map[string]interface{}{"runtime_params": string(encoded)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("PHP software runtime parameters were not updated")
	}
	return nil
}

func persistManagedApacheConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).
		Where("(`key` = ? OR component = ?)", "apache", "apache")
	if strings.TrimSpace(params.Key) != "" {
		query = query.Where("(`key` = ? OR component = ?)", params.Key, "apache")
	}
	if err := query.Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode Apache runtime parameters: %w", err)
		}
	}
	assign := func(valueKey string, runtimeKeys ...string) {
		value := strings.TrimSpace(values[valueKey])
		if value == "" {
			return
		}
		for _, runtimeKey := range runtimeKeys {
			runtime[runtimeKey] = value
		}
	}
	assign("port", "apache-port", "port")
	assign("phpFpmSocket", "apache-php-fpm-socket", "php-fpm-socket")
	assign("webRoot", "apache-web-root", "web-root")
	assign("logDir", "apache-log-dir", "log-dir")
	if runtime["apache-install-dir"] == "" {
		runtime["apache-install-dir"] = "/usr/local/apache"
	}
	if runtime["apache-vhost-root"] == "" {
		runtime["apache-vhost-root"] = "/usr/local/one/vhost"
	}
	if runtime["apache-run-user"] == "" {
		runtime["apache-run-user"] = "www"
	}
	if runtime["apache-run-group"] == "" {
		runtime["apache-run-group"] = "www"
	}
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode Apache runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["port"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("Apache software runtime parameters were not updated")
	}
	return nil
}

func persistManagedMariaDBConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || !strings.EqualFold(strings.TrimSpace(params.Key), "mariadb") || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true).
		Where("(`key` = ? OR component = ?)", "mariadb", "mariadb")
	if err := query.Order("id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode MariaDB runtime parameters: %w", err)
		}
	}
	assign := func(key, persistedKey string) {
		if value := strings.TrimSpace(values[key]); value != "" {
			runtime[persistedKey] = value
		}
	}
	assign("mariadbPort", "mariadb-port")
	assign("bindAddress", "mariadb-bind-address")
	assign("installDir", "install-dir")
	assign("dataDir", "data-dir")
	assign("logDir", "log-dir")
	assign("runUser", "run-user")
	assign("runGroup", "run-group")
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode MariaDB runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["mariadbPort"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("MariaDB software runtime parameters were not updated")
	}
	return nil
}

func persistManagedMySQLConfiguration(params *input.InstallParams, values map[string]string) error {
	if params == nil || !isDatabaseInstallKey(params.Key) || app.DB() == nil {
		return nil
	}
	var row models.Software
	query := app.DB().Where("installed = ?", true)
	if strings.TrimSpace(params.Key) != "" {
		query = query.Where("(`key` = ? OR component = ?)", params.Key, "mysql")
	}
	if err := query.Order("id DESC").First(&row).Error; err != nil {
		return err
	}
	runtime := make(map[string]string)
	if strings.TrimSpace(row.RuntimeParamsJSON) != "" {
		if err := json.Unmarshal([]byte(row.RuntimeParamsJSON), &runtime); err != nil {
			return fmt.Errorf("decode MySQL runtime parameters: %w", err)
		}
	}
	assign := func(key, persistedKey string) {
		if value := strings.TrimSpace(values[key]); value != "" {
			runtime[persistedKey] = value
		}
	}
	assign("mysqlPort", "mysql-port")
	assign("bindAddress", "mysql-bind-address")
	assign("installDir", "install-dir")
	assign("dataDir", "data-dir")
	assign("logDir", "log-dir")
	assign("runUser", "run-user")
	assign("runGroup", "run-group")
	encoded, err := json.Marshal(runtime)
	if err != nil {
		return fmt.Errorf("encode MySQL runtime parameters: %w", err)
	}
	updates := map[string]interface{}{"runtime_params": string(encoded)}
	if port := strings.TrimSpace(values["mysqlPort"]); port != "" {
		updates["http_port"] = port
	}
	result := app.DB().Model(&models.Software{}).Where("id = ?", row.Id).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("MySQL software runtime parameters were not updated")
	}
	return nil
}

func (installer *Installer) resolveConfigurationPackage(
	definition configurationDefinition,
	version string,
) (scriptregistry.Package, error) {
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return scriptregistry.Package{}, err
	}
	componentPackage, err := registry.ResolveInstalledLocal(
		definition.Component,
		strings.TrimSpace(version),
		"configGet",
		"configApply",
	)
	if err != nil {
		return scriptregistry.Package{}, fmt.Errorf(
			"resolve %s configuration package: %w",
			definition.Component,
			err,
		)
	}
	if componentPackage.Manifest.Actions.ConfigGet == "" ||
		componentPackage.Manifest.Actions.ConfigApply == "" {
		return scriptregistry.Package{}, fmt.Errorf(
			"component %s package does not support managed configuration",
			definition.Component,
		)
	}
	return componentPackage, nil
}

func parseComponentConfiguration(
	output []byte,
	definition configurationDefinition,
) (ComponentConfiguration, error) {
	if len(output) == 0 || len(output) > maxConfigurationProbeBytes {
		return ComponentConfiguration{}, errors.New("component configuration output size is invalid")
	}
	allowed := map[string]struct{}{
		"component":  {},
		"revision":   {},
		"apply_mode": {},
	}
	optional := make(map[string]struct{})
	var runtime *ComponentRuntime
	if definition.Component == "mysql" || definition.Component == "mariadb" || definition.Component == "mongodb" || definition.Component == "php" || definition.Component == "firewalld" || definition.Component == "apache" || definition.Component == "openresty" || definition.Component == "caddy" {
		runtime = &ComponentRuntime{}
		runtimeKeys := []string{"runtime.port", "runtime.bindAddress", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup"}
		if definition.Component == "mariadb" {
			runtimeKeys = []string{"runtime.port", "runtime.bindAddress", "runtime.socketPath", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup", "runtime.configFile", "runtime.serviceName", "runtime.version"}
		} else if definition.Component == "php" {
			runtimeKeys = append(runtimeKeys, "runtime.socketPath")
		} else if definition.Component == "openresty" {
			runtimeKeys = []string{"runtime.port", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup"}
		} else if definition.Component == "caddy" {
			runtimeKeys = []string{"runtime.port", "runtime.bindAddress", "runtime.socketPath", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup", "runtime.configFile", "runtime.vhostDir", "runtime.serviceName", "runtime.version"}
		} else if definition.Component == "mongodb" {
			runtimeKeys = []string{"runtime.port", "runtime.bindAddress", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup", "runtime.configFile", "runtime.serviceName", "runtime.version"}
		}
		for _, key := range runtimeKeys {
			allowed[key] = struct{}{}
		}
	}
	if definition.Component == "redis" || definition.Component == "mongodb" {
		for _, key := range []string{
			"connection.port",
			"connection.bindAddress",
			"connection.username",
			"connection.passwordConfigured",
		} {
			allowed[key] = struct{}{}
			optional[key] = struct{}{}
		}
	}
	for _, field := range definition.Fields {
		if !configurationKeyPattern.MatchString(field.Key) {
			return ComponentConfiguration{}, errors.New("managed configuration schema contains an invalid key")
		}
		allowed[field.Key] = struct{}{}
	}
	fields := make(map[string]string, len(allowed))
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxConfigurationProbeBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return ComponentConfiguration{}, errors.New("component configuration output contains an invalid line")
		}
		key := parts[0]
		if _, exists := allowed[key]; !exists {
			return ComponentConfiguration{}, fmt.Errorf("component configuration output contains unknown field %q", key)
		}
		if _, exists := fields[key]; exists {
			return ComponentConfiguration{}, fmt.Errorf("component configuration output contains duplicate field %q", key)
		}
		fields[key] = strings.TrimSpace(parts[1])
	}
	if err := scanner.Err(); err != nil {
		return ComponentConfiguration{}, fmt.Errorf("read component configuration output: %w", err)
	}
	for key := range allowed {
		if _, isOptional := optional[key]; isOptional {
			continue
		}
		if _, exists := fields[key]; !exists {
			return ComponentConfiguration{}, fmt.Errorf("component configuration output is missing field %q", key)
		}
	}
	if fields["component"] != definition.Component ||
		fields["apply_mode"] != definition.ApplyMode ||
		!configurationHashPattern.MatchString(fields["revision"]) {
		return ComponentConfiguration{}, errors.New("component configuration output identity is invalid")
	}
	values := make(map[string]string, len(definition.Fields))
	for _, field := range definition.Fields {
		values[field.Key] = fields[field.Key]
	}
	values, err := normalizeConfigurationValues(definition, values)
	if err != nil {
		return ComponentConfiguration{}, fmt.Errorf("component configuration output is invalid: %w", err)
	}
	if runtime != nil {
		runtime.Port = fields["runtime.port"]
		runtime.BindAddress = fields["runtime.bindAddress"]
		runtime.SocketPath = fields["runtime.socketPath"]
		runtime.InstallDir = fields["runtime.installDir"]
		runtime.DataDir = fields["runtime.dataDir"]
		runtime.LogDir = fields["runtime.logDir"]
		runtime.RunUser = fields["runtime.runUser"]
		runtime.RunGroup = fields["runtime.runGroup"]
		runtime.ConfigFile = fields["runtime.configFile"]
		runtime.VhostDir = fields["runtime.vhostDir"]
		runtime.ServiceName = fields["runtime.serviceName"]
		runtime.Version = fields["runtime.version"]
		if definition.Component == "mariadb" {
			runtime.RuntimeVersion = runtime.Version
		}
		if definition.Component == "mysql" {
			if port, parseErr := strconv.Atoi(runtime.Port); parseErr != nil || port < 1 || port > 65535 {
				return ComponentConfiguration{}, errors.New("component runtime port is invalid")
			}
			if runtime.BindAddress == "" || runtime.InstallDir == "" || runtime.DataDir == "" || runtime.LogDir == "" || runtime.RunUser == "" || runtime.RunGroup == "" {
				return ComponentConfiguration{}, errors.New("component runtime identity is incomplete")
			}
		} else if definition.Component == "mariadb" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 1 || port > 65535 || runtime.BindAddress == "" ||
				runtime.SocketPath != "/run/mariadb/mariadb.sock" ||
				runtime.InstallDir == "" || !strings.HasPrefix(runtime.InstallDir, "/") || filepath.Clean(runtime.InstallDir) != runtime.InstallDir ||
				runtime.DataDir == "" || !strings.HasPrefix(runtime.DataDir, "/") || filepath.Clean(runtime.DataDir) != runtime.DataDir ||
				runtime.LogDir == "" || !strings.HasPrefix(runtime.LogDir, "/") || filepath.Clean(runtime.LogDir) != runtime.LogDir ||
				!systemAccountPattern.MatchString(runtime.RunUser) || !systemAccountPattern.MatchString(runtime.RunGroup) ||
				runtime.ConfigFile != "/etc/oneinstack/mariadb/my.cnf" || runtime.ServiceName != "mariadb" || !phpExactVersionPattern.MatchString(runtime.Version) {
				return ComponentConfiguration{}, errors.New("MariaDB component runtime identity is invalid")
			}
		} else if definition.Component == "mongodb" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 1 || port > 65535 || runtime.BindAddress == "" ||
				runtime.InstallDir == "" || !strings.HasPrefix(runtime.InstallDir, "/") || filepath.Clean(runtime.InstallDir) != runtime.InstallDir ||
				runtime.DataDir == "" || !strings.HasPrefix(runtime.DataDir, "/") || filepath.Clean(runtime.DataDir) != runtime.DataDir ||
				runtime.LogDir == "" || !strings.HasPrefix(runtime.LogDir, "/") || filepath.Clean(runtime.LogDir) != runtime.LogDir ||
				!systemAccountPattern.MatchString(runtime.RunUser) || !systemAccountPattern.MatchString(runtime.RunGroup) ||
				runtime.ConfigFile != "/etc/mongod.conf" || runtime.ServiceName != "mongod" || !runtimeVersionPattern.MatchString(runtime.Version) {
				return ComponentConfiguration{}, errors.New("MongoDB component runtime identity is invalid")
			}
		} else if definition.Component == "php" {
			if runtime.Port != "" || runtime.BindAddress != "unix" || runtime.SocketPath == "" ||
				!strings.HasPrefix(runtime.SocketPath, "/") || filepath.Clean(runtime.SocketPath) != runtime.SocketPath ||
				runtime.InstallDir == "" || runtime.DataDir != "" || runtime.LogDir == "" || runtime.RunUser == "" || runtime.RunGroup == "" {
				return ComponentConfiguration{}, errors.New("PHP component runtime identity is invalid")
			}
		} else if definition.Component == "firewalld" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 0 || port > 65535 || runtime.BindAddress == "" ||
				runtime.InstallDir == "" || !strings.HasPrefix(runtime.InstallDir, "/") || filepath.Clean(runtime.InstallDir) != runtime.InstallDir ||
				runtime.DataDir == "" || !strings.HasPrefix(runtime.DataDir, "/") || filepath.Clean(runtime.DataDir) != runtime.DataDir ||
				runtime.LogDir == "" || runtime.RunUser == "" || runtime.RunGroup == "" {
				return ComponentConfiguration{}, errors.New("firewalld component runtime identity is invalid")
			}
		} else if definition.Component == "apache" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 1 || port > 65535 || runtime.BindAddress != "0.0.0.0" ||
				runtime.InstallDir != "/usr/local/apache" || runtime.DataDir != "" ||
				runtime.LogDir == "" || !strings.HasPrefix(runtime.LogDir, "/") || filepath.Clean(runtime.LogDir) != runtime.LogDir ||
				runtime.RunUser == "" || runtime.RunGroup == "" {
				return ComponentConfiguration{}, errors.New("Apache component runtime identity is invalid")
			}
		} else if definition.Component == "openresty" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 1 || port > 65535 || runtime.BindAddress != "" ||
				runtime.InstallDir == "" || !strings.HasPrefix(runtime.InstallDir, "/") || filepath.Clean(runtime.InstallDir) != runtime.InstallDir ||
				runtime.DataDir != "" || runtime.LogDir == "" || !strings.HasPrefix(runtime.LogDir, "/") || filepath.Clean(runtime.LogDir) != runtime.LogDir ||
				runtime.RunUser == "" || runtime.RunGroup == "" {
				return ComponentConfiguration{}, errors.New("OpenResty component runtime identity is invalid")
			}
		} else if definition.Component == "caddy" {
			port, parseErr := strconv.Atoi(runtime.Port)
			if parseErr != nil || port < 1 || port > 65535 || runtime.BindAddress != "0.0.0.0" ||
				runtime.SocketPath == "" || !strings.HasPrefix(runtime.SocketPath, "/") || filepath.Clean(runtime.SocketPath) != runtime.SocketPath ||
				runtime.InstallDir != "/usr/local/caddy" || runtime.DataDir != "/var/lib/caddy" ||
				runtime.LogDir == "" || !strings.HasPrefix(runtime.LogDir, "/") || filepath.Clean(runtime.LogDir) != runtime.LogDir ||
				runtime.RunUser != "caddy" || runtime.RunGroup != "caddy" ||
				runtime.ConfigFile != "/usr/local/caddy/conf/Caddyfile" || runtime.VhostDir != "/usr/local/one/vhost/caddy" ||
				runtime.ServiceName != "oneinstack-caddy" || !phpExactVersionPattern.MatchString(runtime.Version) {
				return ComponentConfiguration{}, errors.New("Caddy component runtime identity is invalid")
			}
		}
	}
	var connection *ComponentConnection
	if definition.Component == "redis" || definition.Component == "mongodb" {
		connectionKeys := []string{
			"connection.port",
			"connection.bindAddress",
			"connection.username",
			"connection.passwordConfigured",
		}
		connectionFields := 0
		for _, key := range connectionKeys {
			if _, exists := fields[key]; exists {
				connectionFields++
			}
		}
		if connectionFields > 0 && connectionFields != len(connectionKeys) {
			return ComponentConfiguration{}, fmt.Errorf("component %s connection output is incomplete", definition.DisplayName)
		}
		if connectionFields == len(connectionKeys) {
			port, parseErr := strconv.Atoi(fields["connection.port"])
			username := strings.TrimSpace(fields["connection.username"])
			usernameValid := redisUsernamePattern.MatchString(username)
			if definition.Component == "mongodb" {
				usernameValid = mongodbUsernamePattern.MatchString(username)
			}
			if parseErr != nil || port < 1 || port > 65535 || strings.TrimSpace(fields["connection.bindAddress"]) == "" ||
				!usernameValid {
				return ComponentConfiguration{}, fmt.Errorf("component %s connection output is invalid", definition.DisplayName)
			}
			passwordConfigured, parseErr := strconv.ParseBool(fields["connection.passwordConfigured"])
			if parseErr != nil {
				return ComponentConfiguration{}, fmt.Errorf("component %s password status output is invalid", definition.DisplayName)
			}
			connection = &ComponentConnection{
				Port:               strings.TrimSpace(fields["connection.port"]),
				BindAddress:        strings.TrimSpace(fields["connection.bindAddress"]),
				Username:           strings.TrimSpace(fields["connection.username"]),
				PasswordConfigured: &passwordConfigured,
			}
		}
	}
	return ComponentConfiguration{
		Component:   definition.Component,
		SoftwareKey: definition.SoftwareKey,
		DisplayName: definition.DisplayName,
		Revision:    fields["revision"],
		ApplyMode:   definition.ApplyMode,
		Fields:      append([]ConfigurationField(nil), definition.Fields...),
		Values:      values,
		Connection:  connection,
		Runtime:     runtime,
	}, nil
}

func PreviewConfiguration(
	current ComponentConfiguration,
	revision string,
	values map[string]string,
) (ConfigurationPreview, error) {
	revision = strings.TrimSpace(revision)
	if revision != current.Revision {
		return ConfigurationPreview{}, ErrConfigurationConflict
	}
	definition := configurationDefinition{
		Component: current.Component,
		ApplyMode: current.ApplyMode,
		Fields:    append([]ConfigurationField(nil), current.Fields...),
	}
	normalized, err := normalizeConfigurationValues(definition, values)
	if err != nil {
		return ConfigurationPreview{}, err
	}
	preview := ConfigurationPreview{
		Component: current.Component,
		Revision:  current.Revision,
		ApplyMode: current.ApplyMode,
		Values:    normalized,
		Changes:   make([]ConfigurationChange, 0),
	}
	for _, field := range current.Fields {
		before, after := current.Values[field.Key], normalized[field.Key]
		if before == after {
			continue
		}
		preview.Changes = append(preview.Changes, ConfigurationChange{
			Key:    field.Key,
			Label:  field.Label,
			Before: before,
			After:  after,
			Unit:   field.Unit,
		})
	}
	preview.HasChanges = len(preview.Changes) > 0
	return preview, nil
}

// PreviewConfigurationWithContext performs the pure configuration preview and
// additionally checks changed managed port fields against the current host.
// The port is checked only when it changes, because the current service is
// expected to be listening on its existing port.
func PreviewConfigurationWithContext(
	ctx context.Context,
	current ComponentConfiguration,
	revision string,
	values map[string]string,
) (ConfigurationPreview, error) {
	preview, err := PreviewConfiguration(current, revision, values)
	if err != nil {
		return ConfigurationPreview{}, err
	}
	if err := validateConfigurationPortChanges(ctx, current, preview); err != nil {
		return ConfigurationPreview{}, err
	}
	return preview, nil
}

func validateConfigurationPortChanges(
	ctx context.Context,
	current ComponentConfiguration,
	preview ConfigurationPreview,
) error {
	for _, field := range current.Fields {
		if field.Type != "port" && !strings.EqualFold(field.Key, "port") &&
			!strings.EqualFold(field.Key, "listenPort") {
			continue
		}
		before := strings.TrimSpace(current.Values[field.Key])
		after := strings.TrimSpace(preview.Values[field.Key])
		if before == after {
			continue
		}
		port, err := strconv.Atoi(after)
		if err != nil {
			return &InstallParameterError{
				Field:   field.Key,
				Message: "must be a valid port between 1 and 65535",
			}
		}
		if err := validatePortAvailable(ctx, port); err != nil {
			var parameterErr *InstallParameterError
			if errors.As(err, &parameterErr) {
				return &InstallParameterError{Field: field.Key, Message: parameterErr.Message}
			}
			return err
		}
		if current.Component == "mysql" {
			if err := validateManagedMySQLTargetPort(port, before); err != nil {
				return &InstallParameterError{Field: field.Key, Message: err.Error()}
			}
		}
	}
	return nil
}

func validateManagedMySQLTargetPort(port int, currentPort string) error {
	if app.DB() == nil || strconv.Itoa(port) == strings.TrimSpace(currentPort) {
		return nil
	}
	var connection models.Storage
	result := app.DB().
		Where("type = ? AND port = ? AND addr IN ?", "mysql", strconv.Itoa(port), []string{"127.0.0.1", "localhost"}).
		First(&connection)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	if result.Error != nil {
		return fmt.Errorf("check local MySQL connections: %w", result.Error)
	}
	return fmt.Errorf("local MySQL connection already uses port %d", port)
}
