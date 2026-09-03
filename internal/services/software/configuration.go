package software

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
)

const maxConfigurationProbeBytes = 64 * 1024

var (
	ErrConfigurationConflict = errors.New("configuration revision conflict")
	configurationKeyPattern  = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	configurationHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ConfigurationField struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"`
	Unit        string   `json:"unit,omitempty"`
	Description string   `json:"description,omitempty"`
	Min         *int     `json:"min,omitempty"`
	Max         *int     `json:"max,omitempty"`
	Options     []string `json:"options,omitempty"`
}

type ComponentConfiguration struct {
	Component     string               `json:"component"`
	SoftwareKey   string               `json:"softwareKey"`
	DisplayName   string               `json:"displayName"`
	Revision      string               `json:"revision"`
	ApplyMode     string               `json:"applyMode"`
	Fields        []ConfigurationField `json:"fields"`
	Values        map[string]string    `json:"values"`
	PackageSource string               `json:"packageSource"`
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
				Description: "建议保持 auto；手动设置范围为 1–99。",
			},
			integerField("workerConnections", "单进程连接数", "", "每个工作进程允许的最大连接数。", 512, 65535),
			integerField("keepaliveTimeout", "长连接超时", "秒", "客户端 Keep-Alive 空闲超时。", 5, 300),
			integerField("clientMaxBodySize", "请求体上限", "MB", "上传请求允许的最大请求体。", 1, 10240),
		}
		result.Environment = map[string]string{
			"workerProcesses":   "ONEINSTACK_CONFIG_WORKER_PROCESSES",
			"workerConnections": "ONEINSTACK_CONFIG_WORKER_CONNECTIONS",
			"keepaliveTimeout":  "ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT",
			"clientMaxBodySize": "ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE",
		}
	case "mysql":
		result.ApplyMode = "restart"
		result.Fields = []ConfigurationField{
			integerField("maxConnections", "最大连接数", "", "MySQL 同时接受的客户端连接上限。", 10, 100000),
			integerField("maxAllowedPacket", "数据包上限", "MB", "单个通信数据包允许的最大大小。", 1, 1024),
			integerField("innodbBufferPoolSize", "InnoDB 缓冲池", "MB", "建议根据服务器内存和数据库负载设置。", 128, 1048576),
			{Key: "slowQueryLog", Label: "慢查询日志", Type: "boolean", Description: "记录执行时间超过阈值的 SQL。"},
			integerField("longQueryTime", "慢查询阈值", "秒", "超过该时长的查询会进入慢查询日志。", 1, 600),
		}
		result.Environment = map[string]string{
			"maxConnections":       "ONEINSTACK_CONFIG_MAX_CONNECTIONS",
			"maxAllowedPacket":     "ONEINSTACK_CONFIG_MAX_ALLOWED_PACKET",
			"innodbBufferPoolSize": "ONEINSTACK_CONFIG_INNODB_BUFFER_POOL_SIZE",
			"slowQueryLog":         "ONEINSTACK_CONFIG_SLOW_QUERY_LOG",
			"longQueryTime":        "ONEINSTACK_CONFIG_LONG_QUERY_TIME",
		}
	case "php":
		result.ApplyMode = "reload"
		result.Fields = []ConfigurationField{
			integerField("memoryLimit", "脚本内存上限", "MB", "单个 PHP 请求允许使用的最大内存。", 32, 8192),
			integerField("uploadMaxFilesize", "单文件上传上限", "MB", "单个上传文件允许的最大大小。", 1, 2048),
			integerField("postMaxSize", "POST 数据上限", "MB", "必须不小于单文件上传上限。", 1, 4096),
			integerField("maxExecutionTime", "脚本执行超时", "秒", "单个 PHP 脚本的最长执行时间。", 10, 3600),
			integerField("pmMaxChildren", "最大子进程数", "", "PHP-FPM 可同时运行的最大工作进程数。", 1, 10000),
			integerField("pmStartServers", "启动进程数", "", "PHP-FPM 启动时创建的工作进程数。", 1, 10000),
			integerField("pmMinSpareServers", "最小空闲进程", "", "PHP-FPM 保持的最少空闲工作进程。", 1, 10000),
			integerField("pmMaxSpareServers", "最大空闲进程", "", "PHP-FPM 保持的最多空闲工作进程。", 1, 10000),
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
			integerField("maxmemory", "最大内存", "MB", "0 表示不设置 Redis 内存上限。", 0, 1048576),
			{
				Key:         "maxmemoryPolicy",
				Label:       "内存淘汰策略",
				Type:        "select",
				Description: "达到内存上限后 Redis 处理新写入的方式。",
				Options: []string{
					"noeviction", "allkeys-lru", "allkeys-lfu", "allkeys-random",
					"volatile-lru", "volatile-lfu", "volatile-random", "volatile-ttl",
				},
			},
			{Key: "appendonly", Label: "AOF 持久化", Type: "boolean", Description: "将写操作追加到 AOF 文件。"},
			integerField("timeout", "空闲连接超时", "秒", "0 表示不主动断开空闲客户端。", 0, 86400),
			integerField("tcpKeepalive", "TCP Keepalive", "秒", "TCP 保活探测间隔；0 表示关闭。", 0, 3600),
		}
		result.Environment = map[string]string{
			"maxmemory":       "ONEINSTACK_CONFIG_MAXMEMORY",
			"maxmemoryPolicy": "ONEINSTACK_CONFIG_MAXMEMORY_POLICY",
			"appendonly":      "ONEINSTACK_CONFIG_APPENDONLY",
			"timeout":         "ONEINSTACK_CONFIG_TIMEOUT",
			"tcpKeepalive":    "ONEINSTACK_CONFIG_TCP_KEEPALIVE",
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
	if len(values) != len(definition.Fields) {
		return nil, errors.New("configuration must contain every managed field and no unknown fields")
	}
	result := make(map[string]string, len(values))
	for _, field := range definition.Fields {
		value, exists := values[field.Key]
		if !exists {
			return nil, fmt.Errorf("configuration field %s is required", field.Key)
		}
		value = strings.TrimSpace(value)
		if strings.ContainsAny(value, "\x00\r\n") || len(value) > 128 {
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
	return result, nil
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
	definition, err := componentConfigurationDefinition(component)
	if err != nil {
		return ComponentConfiguration{}, err
	}
	componentPackage, err := installer.resolveConfigurationPackage(ctx, definition, version)
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
	params := (&serviceInstallParams{key: definition.SoftwareKey, version: strings.TrimSpace(version)}).input()
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
	return configuration, nil
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
	componentPackage, err := installer.resolveConfigurationPackage(ctx, definition, version)
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
	params := (&serviceInstallParams{key: definition.SoftwareKey, version: strings.TrimSpace(version)}).input()
	installer.setScriptParams(scriptInfo, params)
	scriptInfo.Params["ONEINSTACK_CONFIG_OPERATION"] = "apply"
	scriptInfo.Params["ONEINSTACK_CONFIG_REVISION"] = revision
	for key, value := range normalized {
		scriptInfo.Params[definition.Environment[key]] = value
	}
	return installer.scriptManager.ExecuteScriptTask(ctx, scriptInfo, params, logPath, observer)
}

func (installer *Installer) resolveConfigurationPackage(
	ctx context.Context,
	definition configurationDefinition,
	version string,
) (scriptregistry.Package, error) {
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return scriptregistry.Package{}, err
	}
	componentPackage, err := registry.ResolveInstalled(
		ctx,
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
	return ComponentConfiguration{
		Component:   definition.Component,
		SoftwareKey: definition.SoftwareKey,
		DisplayName: definition.DisplayName,
		Revision:    fields["revision"],
		ApplyMode:   definition.ApplyMode,
		Fields:      append([]ConfigurationField(nil), definition.Fields...),
		Values:      values,
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
