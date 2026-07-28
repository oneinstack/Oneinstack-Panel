package software

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/services/scriptregistry"
)

const maxServiceProbeBytes = 64 * 1024

var (
	serviceStatePattern   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	runtimeVersionPattern = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){1,2}(?:[-+][0-9A-Za-z.-]+)?$`)
)

type ComponentServiceDefinition struct {
	Component   string `json:"component"`
	SoftwareKey string `json:"softwareKey"`
	DisplayName string `json:"displayName"`
	ServiceName string `json:"serviceName"`
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
		{Component: "nginx", SoftwareKey: "webserver", DisplayName: "Nginx", ServiceName: "nginx"},
		{Component: "mysql", SoftwareKey: "db", DisplayName: "MySQL", ServiceName: "mysql"},
		{Component: "php", SoftwareKey: "php", DisplayName: "PHP-FPM", ServiceName: "php-fpm"},
		{Component: "redis", SoftwareKey: "redis", DisplayName: "Redis", ServiceName: "redis"},
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

func IsServiceAction(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "start", "stop", "restart", "reload":
		return true
	default:
		return false
	}
}

func (installer *Installer) InspectService(
	ctx context.Context,
	component string,
	version string,
) (ComponentServiceProbe, error) {
	definition, err := NormalizeServiceComponent(component)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return ComponentServiceProbe{}, err
	}
	componentPackage, err := registry.ResolveInstalled(
		ctx,
		definition.Component,
		strings.TrimSpace(version),
		"status",
	)
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
	if fields["component"] != definition.Component || fields["service"] != definition.ServiceName {
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
