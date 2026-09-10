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

const (
	defaultNginxInstallDir = "/usr/local/nginx"
	defaultNginxStateDir   = "/var/lib/oneinstack/components/nginx"
)

var (
	nginxDefaultListenPattern = regexp.MustCompile(`(?m)^[[:space:]]*listen[[:space:]]+(?:\[::\]:)?([0-9]+)[[:space:]]+default_server[^;]*;`)
	nginxRootPattern          = regexp.MustCompile(`(?m)^[[:space:]]*root[[:space:]]+([^;[:space:]]+);`)
	nginxUserPattern          = regexp.MustCompile(`(?m)^[[:space:]]*user[[:space:]]+([^;[:space:]]+)(?:[[:space:]]+([^;[:space:]]+))?;`)
	nginxErrorLogPattern      = regexp.MustCompile(`(?m)^[[:space:]]*error_log[[:space:]]+([^;[:space:]]+)/nginx-error\.log(?:[[:space:]][^;]*)?;`)
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

func normalizeInstallParameterKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", "-", ".", "-", " ", "-").Replace(value)
	switch value {
	case "nginx-port":
		return "port"
	case "nginx-port-number":
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

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
