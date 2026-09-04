package software

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/scriptregistry"

	"gorm.io/gorm"
)

const maxServiceProbeBytes = 64 * 1024

var (
	serviceStatePattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	serviceNamePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
	runtimeVersionPattern  = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._:+-]{0,63}$`)
	softwareVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,3}(?:[-+][0-9A-Za-z.-]+)?$`)
)

type ComponentServiceDefinition struct {
	Component     string   `json:"component"`
	SoftwareKey   string   `json:"softwareKey"`
	DisplayName   string   `json:"displayName"`
	ServiceName   string   `json:"serviceName"`
	RuntimeGroup  string   `json:"runtimeGroup,omitempty"`
	ManageScopes  []string `json:"manageScopes,omitempty"`
	readinessPort string
}

type ComponentServiceProbe struct {
	Component        string   `json:"component"`
	ServiceName      string   `json:"serviceName"`
	LoadState        string   `json:"loadState"`
	ActiveState      string   `json:"activeState"`
	SubState         string   `json:"subState"`
	UnitFileState    string   `json:"unitFileState"`
	RuntimeVersion   string   `json:"runtimeVersion,omitempty"`
	CanReload        bool     `json:"canReload"`
	AvailableActions []string `json:"availableActions"`
	PackageSource    string   `json:"packageSource"`
}

func SupportedComponentServices() []ComponentServiceDefinition {
	return []ComponentServiceDefinition{
		{Component: "nginx", SoftwareKey: "webserver", DisplayName: "Nginx", ServiceName: "oneinstack-nginx", RuntimeGroup: "web-server", ManageScopes: []string{"web_service"}},
		{Component: "openresty", SoftwareKey: "openresty", DisplayName: "OpenResty", ServiceName: "oneinstack-openresty", RuntimeGroup: "web-server", ManageScopes: []string{"web_service"}},
		{Component: "tengine", SoftwareKey: "tengine", DisplayName: "Tengine", ServiceName: "oneinstack-tengine", RuntimeGroup: "web-server", ManageScopes: []string{"web_service"}},
		{Component: "caddy", SoftwareKey: "caddy", DisplayName: "Caddy", ServiceName: "oneinstack-caddy", RuntimeGroup: "web-server", ManageScopes: []string{"web_service"}},
		{Component: "apache", SoftwareKey: "apache", DisplayName: "Apache HTTP Server", ServiceName: "oneinstack-httpd", RuntimeGroup: "web-server", ManageScopes: []string{"web_service"}},
		{Component: "mysql", SoftwareKey: "db", DisplayName: "MySQL", ServiceName: "mysql", RuntimeGroup: "database", ManageScopes: []string{"database"}},
		{Component: "php", SoftwareKey: "php", DisplayName: "PHP-FPM", ServiceName: "php-fpm", ManageScopes: []string{"runtime"}},
		{Component: "redis", SoftwareKey: "redis", DisplayName: "Redis", ServiceName: "redis-server", ManageScopes: []string{"cache"}},
	}
}

func NormalizeServiceComponent(value string) (ComponentServiceDefinition, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	for _, definition := range SupportedComponentServices() {
		if normalized == definition.Component ||
			normalized == definition.SoftwareKey ||
			normalized == definition.ServiceName {
			return definition, nil
		}
	}
	return ComponentServiceDefinition{}, fmt.Errorf("unsupported component service: %s", value)
}

// ResolveServiceComponent resolves built-in aliases and catalog-managed
// service metadata. Catalog applications without a serviceName remain
// unavailable to service control by default.
func ResolveServiceComponent(database *gorm.DB, value string) (ComponentServiceDefinition, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	legacy, legacyErr := NormalizeServiceComponent(normalized)
	if database == nil {
		return legacy, legacyErr
	}
	var row models.Software
	query := database.Where("installed = ?", true)
	if normalized == "nginx" || normalized == "webserver" {
		query = query.Where(
			"(`key` = ? OR component = ?)",
			"webserver", "nginx",
		)
	} else {
		query = query.Where("(`key` = ? OR component = ?)", normalized, normalized)
	}
	err := query.Order("install_time DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		query = database.Where("catalog_managed = ?", true)
		if normalized == "nginx" || normalized == "webserver" {
			query = query.Where(
				"(`key` = ? OR component = ?)",
				"webserver", "nginx",
			)
		} else {
			query = query.Where("(`key` = ? OR component = ?)", normalized, normalized)
		}
		err = query.Order("catalog_visible DESC, id DESC").First(&row).Error
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ComponentServiceDefinition{}, err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return legacy, legacyErr
	}
	return serviceDefinitionFromRow(row, legacy, legacyErr)
}

// InstalledComponentServices returns the built-in service cards plus any
// installed catalog application that declares a serviceName. This keeps new
// applications out of service control until their package metadata is
// complete, while avoiding a code change for every new component.
func InstalledComponentServices(database *gorm.DB) ([]ComponentServiceDefinition, error) {
	definitions := SupportedComponentServices()
	if database == nil {
		return definitions, nil
	}
	var rows []models.Software
	if err := database.Where("installed = ? AND component <> ''", true).
		Order("install_time DESC, id DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	byComponent := make(map[string]int, len(definitions))
	for index, definition := range definitions {
		byComponent[definition.Component] = index
	}
	for _, row := range rows {
		candidate := strings.TrimSpace(row.Component)
		if candidate == "" {
			candidate = strings.TrimSpace(row.Key)
		}
		legacy, legacyErr := NormalizeServiceComponent(candidate)
		definition, err := serviceDefinitionFromRow(row, legacy, legacyErr)
		if err != nil {
			continue
		}
		if index, exists := byComponent[definition.Component]; exists {
			definitions[index] = definition
			continue
		}
		byComponent[definition.Component] = len(definitions)
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func serviceDefinitionFromRow(
	row models.Software,
	legacy ComponentServiceDefinition,
	legacyErr error,
) (ComponentServiceDefinition, error) {
	if legacyErr == nil {
		definition := legacy
		definition.readinessPort = normalizeServicePort(row.HttpPort)
		if strings.TrimSpace(row.Key) != "" {
			definition.SoftwareKey = strings.ToLower(strings.TrimSpace(row.Key))
		}
		if component := strings.ToLower(strings.TrimSpace(row.Component)); component != "" {
			if normalizedComponent, normalizeErr := NormalizeServiceComponent(component); normalizeErr == nil {
				// Catalog rows may store the software key (for example
				// webserver) in component. Keep the canonical component so
				// installing Apache cannot overwrite the Nginx status entry.
				definition.Component = normalizedComponent.Component
			} else {
				definition.Component = component
			}
		}
		if strings.TrimSpace(row.Name) != "" {
			definition.DisplayName = strings.TrimSpace(row.Name)
		}
		if strings.TrimSpace(row.ServiceName) != "" {
			definition.ServiceName = strings.TrimSpace(row.ServiceName)
		}
		if strings.TrimSpace(row.RuntimeGroup) != "" {
			definition.RuntimeGroup = strings.TrimSpace(row.RuntimeGroup)
		}
		definition.ManageScopes = decodeManageScopes(row.ManageScopesJSON)
		if len(definition.ManageScopes) == 0 {
			definition.ManageScopes = legacy.ManageScopes
		}
		return definition, nil
	}
	serviceName := strings.TrimSpace(row.ServiceName)
	if serviceName == "" || !serviceNamePattern.MatchString(serviceName) {
		return ComponentServiceDefinition{}, fmt.Errorf("service metadata is incomplete for %s", row.Component)
	}
	component := strings.ToLower(strings.TrimSpace(row.Component))
	if component == "" {
		return ComponentServiceDefinition{}, errors.New("service component is required")
	}
	softwareKey := strings.ToLower(strings.TrimSpace(row.Key))
	if softwareKey == "" {
		softwareKey = component
	}
	displayName := strings.TrimSpace(row.Name)
	if displayName == "" {
		displayName = component
	}
	return ComponentServiceDefinition{
		Component: component, SoftwareKey: softwareKey, DisplayName: displayName,
		ServiceName: serviceName, RuntimeGroup: strings.TrimSpace(row.RuntimeGroup),
		ManageScopes: decodeManageScopes(row.ManageScopesJSON), readinessPort: normalizeServicePort(row.HttpPort),
	}, nil
}

func decodeManageScopes(contents string) []string {
	var scopes []string
	if strings.TrimSpace(contents) == "" || json.Unmarshal([]byte(contents), &scopes) != nil {
		return nil
	}
	return scopes
}

func IsServiceAction(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "start", "stop", "restart", "reload":
		return true
	default:
		return false
	}
}

const (
	webServerRuntimeGroup = "web-server"
	phpFPMDefaultSocket   = "/dev/shm/php-cgi.sock"
	serviceReadyTimeout   = 5 * time.Second
)

// verifyServiceActionReady checks the runtime result of a successful service
// script. A systemd command can return successfully before the process has
// created its listener or FastCGI socket, so service state alone is not enough
// for production traffic.
func verifyServiceActionReady(
	ctx context.Context,
	definition ComponentServiceDefinition,
	action string,
) error {
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "start" && action != "restart" && action != "reload" {
		return nil
	}
	serviceName := strings.TrimSpace(definition.ServiceName)
	if serviceName == "" {
		return fmt.Errorf("%s action verification failed: service name is missing", action)
	}
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", serviceName+".service").Run(); err != nil {
		return fmt.Errorf("%s action verification failed: service %s is not active", action, serviceName)
	}

	readyCtx, cancel := context.WithTimeout(ctx, serviceReadyTimeout)
	defer cancel()
	if strings.EqualFold(strings.TrimSpace(definition.Component), "php") {
		if err := waitForPHPFPMReady(readyCtx); err != nil {
			return fmt.Errorf("%s action verification failed: PHP-FPM socket is not ready: %w", action, err)
		}
	}
	if ports := serviceReadinessPorts(definition); len(ports) > 0 {
		if err := waitForServiceListener(readyCtx, ports); err != nil {
			return fmt.Errorf("%s action verification failed: service listener is not ready: %w", action, err)
		}
	}
	return nil
}

func waitForPHPFPMReady(ctx context.Context) error {
	paths := []string{phpFPMDefaultSocket, "/run/php/php-fpm.sock"}
	if matches, err := filepath.Glob("/run/php/php*-fpm.sock"); err == nil {
		paths = append(paths, matches...)
	}
	paths = uniqueStrings(paths)
	var lastErr error
	for {
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				lastErr = err
				continue
			}
			if info.Mode()&os.ModeSocket == 0 {
				lastErr = fmt.Errorf("%s is not a Unix socket", path)
				continue
			}
			probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			connection, err := (&net.Dialer{}).DialContext(probeCtx, "unix", path)
			cancel()
			if err == nil {
				_ = connection.Close()
				return nil
			}
			lastErr = err
		}
		if !waitForReadinessRetry(ctx) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no supported PHP-FPM socket was found")
	}
	return lastErr
}

func waitForServiceListener(ctx context.Context, ports []int) error {
	var lastErr error
	for {
		for _, port := range ports {
			probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			connection, err := (&net.Dialer{}).DialContext(
				probeCtx,
				"tcp",
				fmt.Sprintf("127.0.0.1:%d", port),
			)
			cancel()
			if err == nil {
				_ = connection.Close()
				return nil
			}
			lastErr = err
		}
		if !waitForReadinessRetry(ctx) {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("ports 80 and 443 are not accepting TCP connections")
	}
	return lastErr
}

// serviceReadinessPorts returns the listener ports that should be checked
// after a managed service action. Component installation records provide the
// generic port source, while Nginx additionally reads its current managed
// configuration so a post-install port change is detected immediately.
func serviceReadinessPorts(definition ComponentServiceDefinition) []int {
	if strings.EqualFold(strings.TrimSpace(definition.Component), "nginx") ||
		strings.EqualFold(strings.TrimSpace(definition.SoftwareKey), "webserver") {
		if values := detectNginxInstallParameters(); values != nil {
			if port, ok := parseServicePort(values["port"]); ok {
				return []int{port}
			}
		}
	}
	if port, ok := parseServicePort(definition.readinessPort); ok {
		return []int{port}
	}
	if definition.RuntimeGroup == webServerRuntimeGroup {
		return []int{80, 443}
	}
	return nil
}

func normalizeServicePort(value string) string {
	port, ok := parseServicePort(value)
	if !ok {
		return ""
	}
	return strconv.Itoa(port)
}

func parseServicePort(value string) (int, bool) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	return port, err == nil && port >= 1 && port <= 65535
}

func waitForReadinessRetry(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

type RuntimeGroupOwner struct {
	Component   string `json:"component"`
	ServiceName string `json:"serviceName"`
}

// RuntimeGroupOwnerComponent returns the logical component represented by a
// runtime-group owner. Legacy web units are kept as explicit unmanaged owners
// for lifecycle safety, but their status should still be associated with the
// Web engine that owns their ExecStart binary.
func RuntimeGroupOwnerComponent(ctx context.Context, owner RuntimeGroupOwner) string {
	if owner.Component != "legacy-web" {
		return owner.Component
	}
	execStart, err := exec.CommandContext(
		ctx,
		"systemctl",
		"show",
		owner.ServiceName+".service",
		"--property=ExecStart",
		"--value",
	).Output()
	if err != nil {
		return owner.Component
	}
	command := strings.ToLower(string(execStart))
	switch {
	case strings.Contains(command, "/apache/") || strings.Contains(command, "httpd"):
		return "apache"
	case strings.Contains(command, "/openresty/"):
		return "openresty"
	case strings.Contains(command, "/tengine/"):
		return "tengine"
	case strings.Contains(command, "nginx"):
		return "nginx"
	default:
		return owner.Component
	}
}

// ActiveRuntimeGroupOwners checks the actual systemd units for a runtime
// group, including legacy web units kept for safe migration detection.
func ActiveRuntimeGroupOwners(ctx context.Context, runtimeGroup, excludeComponent string) []RuntimeGroupOwner {
	if strings.TrimSpace(runtimeGroup) != "web-server" {
		return nil
	}
	owners := []RuntimeGroupOwner{
		{Component: "nginx", ServiceName: "oneinstack-nginx"},
		{Component: "openresty", ServiceName: "oneinstack-openresty"},
		{Component: "tengine", ServiceName: "oneinstack-tengine"},
		{Component: "caddy", ServiceName: "oneinstack-caddy"},
		{Component: "apache", ServiceName: "oneinstack-httpd"},
		{Component: "legacy-web", ServiceName: "nginx"},
		{Component: "legacy-web", ServiceName: "httpd"},
		{Component: "legacy-web", ServiceName: "apache2"},
		{Component: "legacy-web", ServiceName: "openresty"},
		{Component: "legacy-web", ServiceName: "tengine"},
		{Component: "legacy-web", ServiceName: "caddy"},
	}
	result := make([]RuntimeGroupOwner, 0, len(owners))
	for _, owner := range owners {
		if owner.Component == excludeComponent {
			continue
		}
		if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", owner.ServiceName+".service").Run(); err == nil {
			result = append(result, owner)
		}
	}
	return result
}

func (installer *Installer) InspectService(
	ctx context.Context,
	component string,
	version string,
) (ComponentServiceProbe, error) {
	return installer.inspectService(ctx, component, version, false)
}

// InspectServiceLocal performs the same verified status probe as
// InspectService but only uses an already cached or bundled package. Scheduled
// health monitoring must remain independent from Center availability.
func (installer *Installer) InspectServiceLocal(
	ctx context.Context,
	component string,
	version string,
) (ComponentServiceProbe, error) {
	return installer.inspectService(ctx, component, version, true)
}

func (installer *Installer) inspectService(
	ctx context.Context,
	component string,
	version string,
	localOnly bool,
) (ComponentServiceProbe, error) {
	definition, err := ResolveServiceComponent(app.DB(), component)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	var componentPackage scriptregistry.Package
	if localOnly {
		componentPackage, err = registry.ResolveInstalledLocal(
			definition.Component,
			strings.TrimSpace(version),
			"status",
		)
	} else {
		componentPackage, err = registry.ResolveInstalled(
			ctx,
			definition.Component,
			strings.TrimSpace(version),
			"status",
		)
	}
	if err != nil {
		return ComponentServiceProbe{}, fmt.Errorf("resolve %s status package: %w", definition.Component, err)
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, "status")
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	installParams := &serviceInstallParams{
		key:     definition.SoftwareKey,
		version: strings.TrimSpace(version),
	}
	params := installParams.input()
	installer.setScriptParams(scriptInfo, params)
	output, err := installer.scriptManager.ExecuteProbe(ctx, scriptInfo, maxServiceProbeBytes)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	probe, err := parseComponentServiceProbe(output, definition)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	if probe.RuntimeVersion == "" && softwareVersionPattern.MatchString(strings.TrimSpace(version)) {
		probe.RuntimeVersion = strings.TrimSpace(version)
	}
	probe.PackageSource = componentPackage.Source
	probe.AvailableActions = availableServiceActions(componentPackage)
	probe.CanReload = probe.CanReload && componentPackage.Manifest.Actions.Reload != ""
	return probe, nil
}

func availableServiceActions(componentPackage scriptregistry.Package) []string {
	actions := []struct {
		name string
		path string
	}{
		{name: "start", path: componentPackage.Manifest.Actions.Start},
		{name: "stop", path: componentPackage.Manifest.Actions.Stop},
		{name: "restart", path: componentPackage.Manifest.Actions.Restart},
		{name: "reload", path: componentPackage.Manifest.Actions.Reload},
	}
	result := make([]string, 0, len(actions))
	for _, action := range actions {
		if strings.TrimSpace(action.path) != "" {
			result = append(result, action.name)
		}
	}
	return result
}

func parseComponentServiceProbe(
	output []byte,
	definition ComponentServiceDefinition,
) (ComponentServiceProbe, error) {
	if len(output) == 0 || len(output) > maxServiceProbeBytes {
		return ComponentServiceProbe{}, fmt.Errorf("component status output size is invalid")
	}
	if legacyState := strings.TrimSpace(string(output)); legacyState == "running" || legacyState == "stopped" {
		if legacyState == "running" {
			return ComponentServiceProbe{
				Component:     definition.Component,
				ServiceName:   definition.ServiceName,
				LoadState:     "loaded",
				ActiveState:   "active",
				SubState:      "running",
				UnitFileState: "unknown",
			}, nil
		}
		return ComponentServiceProbe{
			Component:     definition.Component,
			ServiceName:   definition.ServiceName,
			LoadState:     "loaded",
			ActiveState:   "inactive",
			SubState:      "dead",
			UnitFileState: "unknown",
		}, nil
	}
	fields := make(map[string]string, 8)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxServiceProbeBytes)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return ComponentServiceProbe{}, fmt.Errorf("component status output contains an invalid line")
		}
		key := parts[0]
		switch key {
		case "component", "service", "load_state", "active_state", "sub_state",
			"unit_file_state", "runtime_version", "can_reload":
		case "port", "bind_address", "install_dir", "data_dir", "log_dir", "run_user", "run_group":
			// The managed MySQL status script also reports its effective runtime
			// metadata. These fields are intentionally accepted only for MySQL;
			// they are not part of the public probe response.
			if definition.Component != "mysql" {
				return ComponentServiceProbe{}, fmt.Errorf("component status output contains unknown field %q", key)
			}
		default:
			return ComponentServiceProbe{}, fmt.Errorf("component status output contains unknown field %q", key)
		}
		if _, exists := fields[key]; exists {
			return ComponentServiceProbe{}, fmt.Errorf("component status output contains duplicate field %q", key)
		}
		fields[key] = strings.TrimSpace(parts[1])
	}
	if err := scanner.Err(); err != nil {
		return ComponentServiceProbe{}, fmt.Errorf("read component status output: %w", err)
	}
	for _, key := range []string{
		"component", "service", "load_state", "active_state", "sub_state",
		"unit_file_state", "runtime_version", "can_reload",
	} {
		if _, exists := fields[key]; !exists {
			return ComponentServiceProbe{}, fmt.Errorf("component status output is missing field %q", key)
		}
	}
	if fields["component"] != definition.Component ||
		!serviceProbeIdentityMatches(definition, fields["service"]) {
		return ComponentServiceProbe{}, fmt.Errorf("component status output identity does not match request")
	}
	for _, key := range []string{"load_state", "active_state", "sub_state", "unit_file_state"} {
		if !serviceStatePattern.MatchString(fields[key]) {
			return ComponentServiceProbe{}, fmt.Errorf("component status output contains invalid %s", key)
		}
	}
	if fields["runtime_version"] != "" &&
		!runtimeVersionPattern.MatchString(fields["runtime_version"]) {
		return ComponentServiceProbe{}, fmt.Errorf("component status output contains invalid runtime version")
	}
	canReload, err := strconv.ParseBool(fields["can_reload"])
	if err != nil {
		return ComponentServiceProbe{}, fmt.Errorf("component status output contains invalid can_reload")
	}
	return ComponentServiceProbe{
		Component:      definition.Component,
		ServiceName:    definition.ServiceName,
		LoadState:      fields["load_state"],
		ActiveState:    fields["active_state"],
		SubState:       fields["sub_state"],
		UnitFileState:  fields["unit_file_state"],
		RuntimeVersion: fields["runtime_version"],
		CanReload:      canReload,
	}, nil
}

func serviceProbeIdentityMatches(
	definition ComponentServiceDefinition,
	serviceName string,
) bool {
	if serviceName == definition.ServiceName {
		return true
	}
	// Redis 7.4.8 packages installed the verified redis.service unit and emit
	// service=redis. Newer packages use the canonical redis-server identity.
	// Accept only this known legacy identity and normalize the API response to
	// definition.ServiceName in parseComponentServiceProbe.
	return (definition.Component == "redis" &&
		definition.ServiceName == "redis-server" && serviceName == "redis") ||
		(definition.Component == "nginx" && serviceName == "nginx")
}

func ClassifyServiceProbeError(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "probe_timeout", "状态探测超时"
	case strings.Contains(err.Error(), "missing field"),
		strings.Contains(err.Error(), "duplicate field"),
		strings.Contains(err.Error(), "invalid line"),
		strings.Contains(err.Error(), "unknown field"),
		strings.Contains(err.Error(), "invalid runtime version"),
		strings.Contains(err.Error(), "invalid can_reload"),
		strings.Contains(err.Error(), "identity does not match request"):
		return "probe_output_invalid", "状态探针输出格式异常"
	case strings.Contains(err.Error(), "resolve"),
		strings.Contains(err.Error(), "package"),
		strings.Contains(err.Error(), "script"):
		return "probe_package_unavailable", "状态探针脚本不可用"
	default:
		return "probe_execution_failed", "状态探测失败"
	}
}
