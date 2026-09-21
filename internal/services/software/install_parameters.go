package software

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"oneinstack/router/output"
)

func hydratePersistedInstallParameters(runtimeJSON string, params []*output.SoftParam) {
	if strings.TrimSpace(runtimeJSON) == "" {
		return
	}
	values := make(map[string]string)
	if json.Unmarshal([]byte(runtimeJSON), &values) != nil {
		return
	}
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		target := compactInstallParameterName(parameter.Key)
		for key, value := range values {
			candidate := compactInstallParameterName(key)
			if candidate == target || (target == "port" && strings.HasSuffix(candidate, "port")) ||
				(candidate == "port" && strings.HasSuffix(target, "port")) {
				if strings.TrimSpace(value) != "" {
					parameter.Default = value
				}
				break
			}
		}
	}
}

const (
	defaultNginxInstallDir     = "/usr/local/nginx"
	defaultNginxStateDir       = "/var/lib/oneinstack/components/nginx"
	defaultOpenRestyInstallDir = "/usr/local/openresty"
	defaultOpenRestyStateDir   = "/var/lib/oneinstack/components/openresty"
	defaultTengineInstallDir   = "/usr/local/tengine"
	defaultTengineStateDir     = "/var/lib/oneinstack/components/tengine"
	defaultCaddyStateDir       = "/var/lib/oneinstack/components/caddy"
	defaultApacheInstallDir    = "/usr/local/apache"
	defaultApacheStateDir      = "/var/lib/oneinstack/components/apache"
)

var (
	nginxDefaultListenPattern = regexp.MustCompile(`(?m)^[[:space:]]*listen[[:space:]]+(?:\[::\]:)?([0-9]+)[[:space:]]+default_server[^;]*;`)
	nginxRootPattern          = regexp.MustCompile(`(?m)^[[:space:]]*root[[:space:]]+([^;[:space:]]+);`)
	nginxUserPattern          = regexp.MustCompile(`(?m)^[[:space:]]*user[[:space:]]+([^;[:space:]]+)(?:[[:space:]]+([^;[:space:]]+))?;`)
	nginxErrorLogPattern      = regexp.MustCompile(`(?m)^[[:space:]]*error_log[[:space:]]+([^;[:space:]]+)/nginx-error\.log(?:[[:space:]][^;]*)?;`)
	openRestyErrorLogPattern  = regexp.MustCompile(`(?m)^[[:space:]]*error_log[[:space:]]+([^;[:space:]]+)/openresty-error\.log(?:[[:space:]][^;]*)?;`)
	tengineErrorLogPattern    = regexp.MustCompile(`(?m)^[[:space:]]*error_log[[:space:]]+([^;[:space:]]+)/tengine-error\.log(?:[[:space:]][^;]*)?;`)
	tenginePHPFPMPathPattern  = regexp.MustCompile(`(?m)^[[:space:]]*fastcgi_pass[[:space:]]+unix:([^;[:space:]]+);`)
	apacheListenPattern       = regexp.MustCompile(`(?m)^[[:space:]]*Listen[[:space:]]+(?:\[[^]]+\]|[^[:space:]:]+:)?([0-9]+)(?:[[:space:]]+#.*)?[[:space:]]*$`)
	apacheDocumentRootPattern = regexp.MustCompile(`(?m)^[[:space:]]*DocumentRoot[[:space:]]+"([^"]+)/default"`)
	apacheErrorLogPattern     = regexp.MustCompile(`(?m)^[[:space:]]*ErrorLog[[:space:]]+"([^"]+)/apache-error\.log"`)
)

// detectNginxInstallParameters reads only the managed Nginx parameter file and
// its current/preserved configuration. It is intentionally read-only and
// returns an empty map when a host has no discoverable managed configuration.
func detectNginxInstallParameters() map[string]string {
	values := make(map[string]string)
	stateDir := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if stateDir == "" {
		stateDir = filepath.Dir(defaultNginxStateDir)
	}
	managedStateDir := filepath.Join(stateDir, "nginx")
	parameterFile := filepath.Join(managedStateDir, "install-parameters")
	for key, value := range readNginxParameterFile(parameterFile) {
		values[normalizeInstallParameterKey(key)] = value
	}
	installDir := values["install-dir"]
	if installDir == "" {
		installDir = nginxInstallDirFromUnit()
	}
	if installDir == "" {
		installDir = defaultNginxInstallDir
	}

	configRoots := []string{
		filepath.Join(installDir, "conf"),
	}
	configRoots = append(configRoots, latestNginxPreservedConfigRoots(managedStateDir)...)
	for _, configRoot := range configRoots {
		mainConfig := filepath.Join(configRoot, "nginx.conf")
		siteConfig := filepath.Join(configRoot, "conf.d", "default.conf")
		if !fileExists(mainConfig) && !fileExists(siteConfig) {
			continue
		}
		readNginxConfiguration(values, mainConfig, siteConfig)
		break
	}
	if installDir != "" {
		values["install-dir"] = installDir
	}
	return values
}

func hydrateNginxInstallParameters(component, key string, params []*output.SoftParam) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(component), "nginx") &&
		!strings.EqualFold(strings.TrimSpace(key), "webserver") {
		return nil
	}
	values := detectNginxInstallParameters()
	for _, parameter := range params {
		if parameter == nil {
			continue
		}
		parameterKey := normalizeInstallParameterKey(parameter.Key)
		if value := values[parameterKey]; value != "" {
			parameter.Default = value
		}
	}
	return values
}

// detectTengineInstallParameters reads the Tengine-owned parameter file and
// native/legacy configuration paths. It never adopts a generic Nginx service.
func detectTengineInstallParameters() map[string]string {
	values := make(map[string]string)
	stateRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if stateRoot == "" {
		stateRoot = filepath.Dir(defaultTengineStateDir)
	}
	managedStateDir := filepath.Join(stateRoot, "tengine")
	parameterFile := filepath.Join(managedStateDir, "install-parameters")
	for key, value := range readNginxParameterFile(parameterFile) {
		values[normalizeInstallParameterKey(key)] = value
	}
	installDir := values["install-dir"]
	if installDir == "" {
		installDir = tengineInstallDirFromUnit()
	}
	if installDir == "" {
		installDir = defaultTengineInstallDir
	}
	configRoots := []string{filepath.Join(installDir, "conf")}
	configRoots = append(configRoots, latestTenginePreservedConfigRoots(managedStateDir)...)
	for _, configRoot := range configRoots {
		mainConfig := filepath.Join(configRoot, "tengine.conf")
		siteConfig := filepath.Join(configRoot, "conf.d", "default.conf")
		if !fileExists(mainConfig) {
			mainConfig = filepath.Join(configRoot, "nginx.conf")
		}
		if !fileExists(mainConfig) && !fileExists(siteConfig) {
			continue
		}
		readTengineConfiguration(values, mainConfig, siteConfig)
		break
	}
	values["install-dir"] = installDir
	return values
}

func hydrateTengineInstallParameters(component, key string, params []*output.SoftParam) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(component), "tengine") &&
		!strings.EqualFold(strings.TrimSpace(key), "tengine") {
		return nil
	}
	values := detectTengineInstallParameters()
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		parameterKey := compactInstallParameterName(parameter.Key)
		var value string
		switch parameterKey {
		case "port", "tengineport":
			value = installParameterValue(values, "port", "tengine-port", "tenginePort")
		case "phpfmpsocket":
			value = installParameterValue(values, "php-fpm-socket", "tengine-php-fpm-socket", "phpFpmSocket")
		case "installdir":
			value = installParameterValue(values, "install-dir", "tengine-install-dir", "installDir")
		case "webroot":
			value = installParameterValue(values, "web-root", "tengine-web-root", "webRoot")
		case "logdir":
			value = installParameterValue(values, "log-dir", "tengine-log-dir", "logDir")
		case "webvhostroot":
			value = installParameterValue(values, "web-vhost-root", "tengine-vhost-root", "webVhostRoot")
		case "runuser":
			value = installParameterValue(values, "run-user", "tengine-run-user", "runUser")
		case "rungroup":
			value = installParameterValue(values, "run-group", "tengine-run-group", "runGroup")
		default:
			value = installParameterValue(values, parameter.Key)
		}
		if value != "" {
			parameter.Default = value
		}
	}
	return values
}

// hydrateCaddyInstallParameters reflects only the non-sensitive values written
// by the managed Caddy lifecycle. Server-owned source and identity fields stay
// internal and therefore cannot be overridden through the request parameter map.
func hydrateCaddyInstallParameters(component, key, runtimeJSON string, params []*output.SoftParam) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(component), "caddy") &&
		!strings.EqualFold(strings.TrimSpace(key), "caddy") {
		return nil
	}
	values := make(map[string]string)
	if strings.TrimSpace(runtimeJSON) != "" {
		var runtime map[string]string
		if json.Unmarshal([]byte(runtimeJSON), &runtime) == nil {
			for runtimeKey, value := range runtime {
				values[normalizeInstallParameterKey(runtimeKey)] = value
			}
		}
	}
	stateRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if stateRoot == "" {
		stateRoot = filepath.Dir(defaultCaddyStateDir)
	}
	for parameterKey, value := range readNginxParameterFile(filepath.Join(stateRoot, "caddy", "install-parameters")) {
		values[normalizeInstallParameterKey(parameterKey)] = value
	}
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		parameterKey := compactInstallParameterName(parameter.Key)
		var value string
		switch parameterKey {
		case "port", "caddyport":
			value = installParameterValue(values, "port", "caddy-port", "caddyPort")
		case "phpfmpsocket":
			value = installParameterValue(values, "php-fpm-socket", "caddy-php-fpm-socket", "phpFpmSocket")
		case "webroot":
			value = installParameterValue(values, "web-root", "caddy-web-root", "webRoot")
		case "logdir":
			value = installParameterValue(values, "log-dir", "caddy-log-dir", "logDir")
		default:
			value = installParameterValue(values, parameter.Key)
		}
		if value != "" {
			parameter.Default = value
		}
	}
	return values
}

// hydrateOpenRestyInstallParameters reflects the managed OpenResty prefix and
// its native nginx subtree without falling back to package-manager locations.
func hydrateOpenRestyInstallParameters(component, key string, params []*output.SoftParam) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(component), "openresty") &&
		!strings.EqualFold(strings.TrimSpace(key), "openresty") {
		return nil
	}
	values := detectOpenRestyInstallParameters()
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		parameterKey := compactInstallParameterName(parameter.Key)
		var value string
		switch parameterKey {
		case "port", "openrestyport":
			value = installParameterValue(values, "port", "openresty-port", "openrestyPort")
		case "phpfmpsocket":
			value = installParameterValue(values, "php-fpm-socket", "openresty-php-fpm-socket", "phpFpmSocket")
		case "installdir":
			value = installParameterValue(values, "install-dir", "openresty-install-dir", "installDir")
		case "webroot":
			value = installParameterValue(values, "web-root", "openresty-web-root", "webRoot")
		case "logdir":
			value = installParameterValue(values, "log-dir", "openresty-log-dir", "logDir")
		case "webvhostroot":
			value = installParameterValue(values, "web-vhost-root", "openresty-vhost-root", "webVhostRoot")
		case "runuser":
			value = installParameterValue(values, "run-user", "openresty-run-user", "runUser")
		case "rungroup":
			value = installParameterValue(values, "run-group", "openresty-run-group", "runGroup")
		default:
			value = installParameterValue(values, parameter.Key)
		}
		if value != "" {
			parameter.Default = value
		}
	}
	return values
}

func detectOpenRestyInstallParameters() map[string]string {
	values := make(map[string]string)
	stateRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if stateRoot == "" {
		stateRoot = filepath.Dir(defaultOpenRestyStateDir)
	}
	managedStateDir := filepath.Join(stateRoot, "openresty")
	for key, value := range readNginxParameterFile(filepath.Join(managedStateDir, "install-parameters")) {
		values[normalizeInstallParameterKey(key)] = value
	}
	installDir := values["install-dir"]
	if installDir == "" {
		installDir = openRestyInstallDirFromUnit()
	}
	if installDir == "" {
		installDir = defaultOpenRestyInstallDir
	}
	configRoots := []string{filepath.Join(installDir, "nginx", "conf")}
	configRoots = append(configRoots, latestOpenRestyPreservedConfigRoots(managedStateDir)...)
	for _, configRoot := range configRoots {
		mainConfig := filepath.Join(configRoot, "nginx.conf")
		siteConfig := filepath.Join(configRoot, "conf.d", "default.conf")
		if !fileExists(mainConfig) && !fileExists(siteConfig) {
			continue
		}
		readOpenRestyConfiguration(values, mainConfig, siteConfig)
		break
	}
	values["install-dir"] = installDir
	return values
}

// hydrateRedisInstallParameters reflects the effective non-secret Redis
// installation parameters in the software list. The values are persisted by
// the script manager after manifest defaults and request overrides are
// resolved, so reopening the install/configuration view does not fall back to
// stale catalog defaults.
func hydrateRedisInstallParameters(component, key, runtimeJSON string, params []*output.SoftParam) {
	if !strings.EqualFold(strings.TrimSpace(component), "redis") &&
		!strings.EqualFold(strings.TrimSpace(key), "redis") {
		return
	}
	if strings.TrimSpace(runtimeJSON) == "" {
		return
	}
	values := make(map[string]string)
	if json.Unmarshal([]byte(runtimeJSON), &values) != nil {
		return
	}
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		parameterKey := compactInstallParameterName(parameter.Key)
		var value string
		switch parameterKey {
		case "port", "redisport":
			value = installParameterValue(values, "redis-port", "redisPort", "port")
		case "redisbind":
			value = installParameterValue(values, "redis-bind", "redisBind")
		case "redisusername":
			value = installParameterValue(values, "redis-username", "redisUsername", "username")
		default:
			value = installParameterValue(values, parameter.Key)
		}
		if value != "" {
			parameter.Default = value
		}
	}
}

func detectApacheInstallParameters() map[string]string {
	values := make(map[string]string)
	stateRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_COMPONENT_STATE"))
	if stateRoot == "" {
		stateRoot = filepath.Dir(defaultApacheStateDir)
	}
	stateDir := filepath.Join(stateRoot, "apache")
	parameterFile := filepath.Join(stateDir, "install-parameters")
	for key, value := range readNginxParameterFile(parameterFile) {
		values[normalizeInstallParameterKey(key)] = value
	}
	installDir := values["install-dir"]
	if installDir == "" {
		installDir = defaultApacheInstallDir
	}
	mainConfig := filepath.Join(installDir, "conf", "httpd.conf")
	contents, err := os.ReadFile(mainConfig)
	if err == nil {
		if match := apacheListenPattern.FindSubmatch(contents); len(match) > 1 {
			values["port"] = string(match[1])
		}
		if match := apacheDocumentRootPattern.FindSubmatch(contents); len(match) > 1 {
			values["web-root"] = normalizeNginxWebRoot(string(match[1]))
		}
		if match := apacheErrorLogPattern.FindSubmatch(contents); len(match) > 1 {
			values["log-dir"] = string(match[1])
		}
	}
	values["install-dir"] = installDir
	return values
}

func hydrateApacheInstallParameters(component, key, runtimeJSON string, params []*output.SoftParam) map[string]string {
	if !strings.EqualFold(strings.TrimSpace(component), "apache") &&
		!strings.EqualFold(strings.TrimSpace(key), "apache") {
		return nil
	}
	values := make(map[string]string)
	if strings.TrimSpace(runtimeJSON) != "" {
		var runtime map[string]string
		if json.Unmarshal([]byte(runtimeJSON), &runtime) == nil {
			for runtimeKey, value := range runtime {
				values[normalizeInstallParameterKey(runtimeKey)] = value
			}
		}
	}
	// The current Apache configuration is authoritative for mutable runtime
	// fields. RuntimeParamsJSON is retained as a fallback for values that are
	// not safely recoverable from httpd.conf, such as the PHP-FPM socket.
	for detectedKey, detectedValue := range detectApacheInstallParameters() {
		if strings.TrimSpace(detectedValue) != "" {
			values[normalizeInstallParameterKey(detectedKey)] = detectedValue
		}
	}
	for _, parameter := range params {
		if parameter == nil || strings.EqualFold(strings.TrimSpace(parameter.Types), "password") {
			continue
		}
		parameterKey := compactInstallParameterName(parameter.Key)
		var value string
		switch parameterKey {
		case "port", "apacheport":
			value = installParameterValue(values, "port", "apache-port", "apachePort")
		case "phpfmpsocket":
			value = installParameterValue(values, "php-fpm-socket", "apache-php-fpm-socket", "phpFpmSocket")
		case "installdir":
			value = installParameterValue(values, "install-dir", "apache-install-dir", "installDir")
		case "webroot":
			value = installParameterValue(values, "web-root", "apache-web-root", "webRoot")
		case "logdir":
			value = installParameterValue(values, "log-dir", "apache-log-dir", "logDir")
		case "webvhostroot":
			value = installParameterValue(values, "web-vhost-root", "apache-vhost-root", "webVhostRoot")
		case "runuser":
			value = installParameterValue(values, "run-user", "apache-run-user", "runUser")
		case "rungroup":
			value = installParameterValue(values, "run-group", "apache-run-group", "runGroup")
		default:
			value = installParameterValue(values, parameter.Key)
		}
		if value != "" {
			parameter.Default = value
		}
	}
	return values
}

func normalizeInstallParameterKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
	switch value {
	case "nginx-port", "openresty-port", "apache-port", "tengine-port", "caddy-port":
		return "port"
	case "nginx-port-number", "openresty-port-number", "apache-port-number", "tengine-port-number", "caddy-port-number":
		return "port"
	default:
		return value
	}
}

func readNginxParameterFile(path string) map[string]string {
	values := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		values[key] = value
	}
	return values
}

func readNginxConfiguration(values map[string]string, mainConfig, siteConfig string) {
	mainContents, mainErr := os.ReadFile(mainConfig)
	if mainErr == nil {
		if match := nginxUserPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["run-user"] = string(match[1])
			if len(match) > 2 && len(match[2]) > 0 {
				values["run-group"] = string(match[2])
			}
		}
		if match := nginxErrorLogPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["log-dir"] = string(match[1])
		}
	}
	siteContents, siteErr := os.ReadFile(siteConfig)
	if siteErr != nil {
		return
	}
	if match := nginxDefaultListenPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["port"] = string(match[1])
	}
	if match := nginxRootPattern.FindSubmatch(siteContents); len(match) > 1 {
		root := string(match[1])
		values["web-root"] = normalizeNginxWebRoot(root)
	}
}

func readTengineConfiguration(values map[string]string, mainConfig, siteConfig string) {
	mainContents, mainErr := os.ReadFile(mainConfig)
	if mainErr == nil {
		if match := nginxUserPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["run-user"] = string(match[1])
			if len(match) > 2 && len(match[2]) > 0 {
				values["run-group"] = string(match[2])
			}
		}
		if match := tengineErrorLogPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["log-dir"] = string(match[1])
		}
	}
	siteContents, siteErr := os.ReadFile(siteConfig)
	if siteErr != nil {
		return
	}
	if match := nginxDefaultListenPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["port"] = string(match[1])
	}
	if match := nginxRootPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["web-root"] = normalizeNginxWebRoot(string(match[1]))
	}
	if match := tenginePHPFPMPathPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["php-fpm-socket"] = string(match[1])
	}
}

func readOpenRestyConfiguration(values map[string]string, mainConfig, siteConfig string) {
	mainContents, mainErr := os.ReadFile(mainConfig)
	if mainErr == nil {
		if match := nginxUserPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["run-user"] = string(match[1])
			if len(match) > 2 && len(match[2]) > 0 {
				values["run-group"] = string(match[2])
			}
		}
		if match := openRestyErrorLogPattern.FindSubmatch(mainContents); len(match) > 1 {
			values["log-dir"] = string(match[1])
		}
	}
	siteContents, siteErr := os.ReadFile(siteConfig)
	if siteErr != nil {
		return
	}
	if match := nginxDefaultListenPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["port"] = string(match[1])
	}
	if match := nginxRootPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["web-root"] = normalizeNginxWebRoot(string(match[1]))
	}
	if match := tenginePHPFPMPathPattern.FindSubmatch(siteContents); len(match) > 1 {
		values["php-fpm-socket"] = string(match[1])
	}
}

func normalizeNginxWebRoot(root string) string {
	root = filepath.Clean(strings.TrimSpace(root))
	const managedWebRoot = "/data/wwwroot"
	if strings.HasPrefix(root, managedWebRoot+string(filepath.Separator)) {
		remainder := strings.TrimPrefix(root, managedWebRoot+string(filepath.Separator))
		allDefault := remainder != ""
		for _, segment := range strings.Split(remainder, string(filepath.Separator)) {
			if segment != "default" {
				allDefault = false
				break
			}
		}
		if allDefault {
			return managedWebRoot
		}
	}
	return strings.TrimSuffix(root, "/default")
}

func latestNginxPreservedConfigRoots(stateDir string) []string {
	roots := make([]string, 0, 8)
	patterns := []string{
		filepath.Join(stateDir, "removed", "*", "install", "conf"),
		filepath.Join(strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")), "nginx", "*", "config"),
	}
	if strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")) == "" {
		patterns[1] = filepath.Join("/var/lib/oneinstack/web-server-migration/nginx", "*", "config")
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		sort.Strings(matches)
		for index := len(matches) - 1; index >= 0; index-- {
			roots = append(roots, matches[index])
		}
	}
	return roots
}

func latestTenginePreservedConfigRoots(stateDir string) []string {
	roots := make([]string, 0, 8)
	patterns := []string{
		filepath.Join(stateDir, "removed", "*", "install", "conf"),
		filepath.Join(strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")), "tengine", "*", "config"),
	}
	if strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")) == "" {
		patterns[1] = filepath.Join("/var/lib/oneinstack/web-server-migration/tengine", "*", "config")
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		sort.Strings(matches)
		for index := len(matches) - 1; index >= 0; index-- {
			roots = append(roots, matches[index])
		}
	}
	return roots
}

func latestOpenRestyPreservedConfigRoots(stateDir string) []string {
	roots := make([]string, 0, 8)
	patterns := []string{
		filepath.Join(stateDir, "removed", "*", "install", "nginx", "conf"),
		filepath.Join(strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")), "openresty", "*", "config"),
	}
	if strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_MIGRATION_ROOT")) == "" {
		patterns[1] = filepath.Join("/var/lib/oneinstack/web-server-migration/openresty", "*", "config")
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		sort.Strings(matches)
		for index := len(matches) - 1; index >= 0; index-- {
			roots = append(roots, matches[index])
		}
	}
	return roots
}

func nginxInstallDirFromUnit() string {
	contents, err := os.ReadFile("/etc/systemd/system/oneinstack-nginx.service")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")))
		if len(fields) == 0 {
			continue
		}
		binary := strings.Trim(fields[0], "\"'")
		binary = strings.TrimPrefix(binary, "-")
		if strings.HasSuffix(binary, "/sbin/nginx") {
			return strings.TrimSuffix(binary, "/sbin/nginx")
		}
	}
	return ""
}

func tengineInstallDirFromUnit() string {
	for _, unit := range []string{
		"/etc/systemd/system/oneinstack-tengine.service",
		"/etc/systemd/system/tengine.service",
		"/etc/systemd/system/nginx.service",
	} {
		contents, err := os.ReadFile(unit)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(contents), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "ExecStart=") {
				continue
			}
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")))
			if len(fields) == 0 {
				continue
			}
			binary := strings.TrimPrefix(strings.Trim(fields[0], "\"'"), "-")
			for _, suffix := range []string{"/sbin/tengine", "/sbin/nginx"} {
				if strings.HasSuffix(binary, suffix) && strings.HasPrefix(binary, defaultTengineInstallDir+"/") {
					return strings.TrimSuffix(binary, suffix)
				}
			}
		}
	}
	return ""
}

func openRestyInstallDirFromUnit() string {
	for _, unit := range []string{
		"/etc/systemd/system/oneinstack-openresty.service",
		"/etc/systemd/system/openresty.service",
		"/etc/systemd/system/nginx.service",
	} {
		contents, err := os.ReadFile(unit)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(contents), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "ExecStart=") {
				continue
			}
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "ExecStart=")))
			if len(fields) == 0 {
				continue
			}
			binary := strings.TrimPrefix(strings.Trim(fields[0], "\"'"), "-")
			const suffix = "/nginx/sbin/nginx"
			if strings.HasSuffix(binary, suffix) && strings.HasPrefix(binary, defaultOpenRestyInstallDir+"/") {
				return strings.TrimSuffix(binary, suffix)
			}
		}
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
