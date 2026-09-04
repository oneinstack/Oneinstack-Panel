package software

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"

	"gorm.io/gorm"
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
	Default     string   `json:"default,omitempty"`
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
	Runtime       *ComponentRuntime    `json:"runtime,omitempty"`
}

type ComponentRuntime struct {
	Port        string `json:"port"`
	BindAddress string `json:"bindAddress"`
	InstallDir  string `json:"installDir"`
	DataDir     string `json:"dataDir"`
	LogDir      string `json:"logDir"`
	RunUser     string `json:"runUser"`
	RunGroup    string `json:"runGroup"`
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
		value := strings.TrimSpace(values[field.Key])
		if value == "" && strings.TrimSpace(field.Default) != "" {
			value = strings.TrimSpace(field.Default)
		}
		if value == "" {
			return nil, fmt.Errorf("configuration field %s is required", field.Key)
		}
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
	params := installedServiceInstallParams(
		definition.SoftwareKey,
		definition.Component,
		strings.TrimSpace(version),
	)
	installer.setScriptParams(scriptInfo, params)
	scriptInfo.Params["ONEINSTACK_CONFIG_OPERATION"] = "apply"
	scriptInfo.Params["ONEINSTACK_CONFIG_REVISION"] = revision
	for key, value := range normalized {
		scriptInfo.Params[definition.Environment[key]] = value
	}
	taskID, err := installer.scriptManager.ExecuteScriptTask(ctx, scriptInfo, params, logPath, observer)
	if err != nil {
		return "", err
	}
	if err := persistManagedMySQLConfiguration(params, normalized); err != nil {
		return "", err
	}
	return taskID, nil
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
	var runtime *ComponentRuntime
	if definition.Component == "mysql" {
		runtime = &ComponentRuntime{}
		for _, key := range []string{"runtime.port", "runtime.bindAddress", "runtime.installDir", "runtime.dataDir", "runtime.logDir", "runtime.runUser", "runtime.runGroup"} {
			allowed[key] = struct{}{}
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
		runtime.InstallDir = fields["runtime.installDir"]
		runtime.DataDir = fields["runtime.dataDir"]
		runtime.LogDir = fields["runtime.logDir"]
		runtime.RunUser = fields["runtime.runUser"]
		runtime.RunGroup = fields["runtime.runGroup"]
		if port, parseErr := strconv.Atoi(runtime.Port); parseErr != nil || port < 1 || port > 65535 {
			return ComponentConfiguration{}, errors.New("component runtime port is invalid")
		}
		if runtime.BindAddress == "" || runtime.InstallDir == "" || runtime.DataDir == "" || runtime.LogDir == "" || runtime.RunUser == "" || runtime.RunGroup == "" {
			return ComponentConfiguration{}, errors.New("component runtime identity is incomplete")
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
