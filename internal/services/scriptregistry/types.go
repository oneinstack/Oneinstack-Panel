package scriptregistry

import (
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	componentIDPattern         = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	versionPattern             = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){1,2}(?:[-+][0-9A-Za-z.-]+)?$`)
	softwareVersionPattern     = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){1,2}(?:[-+][0-9A-Za-z.-]+)?$`)
	softwareVersionLinePattern = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+)*\.x$`)
	parameterPattern           = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	configurationKeyPattern    = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	runtimeGroupPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	serviceNamePattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
)

type Manifest struct {
	SchemaVersion int           `json:"schemaVersion" yaml:"schemaVersion"`
	Component     Component     `json:"component" yaml:"component"`
	Compatibility Compatibility `json:"compatibility" yaml:"compatibility"`
	Dependencies  Dependencies  `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
	Conflicts     []string      `json:"conflicts,omitempty" yaml:"conflicts,omitempty"`
	RuntimeGroup  string        `json:"runtimeGroup,omitempty" yaml:"runtimeGroup,omitempty"`
	ServiceName   string        `json:"serviceName,omitempty" yaml:"serviceName,omitempty"`
	Actions       Actions       `json:"actions" yaml:"actions"`
	Parameters    []Parameter   `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	Configuration Configuration `json:"configuration,omitempty" yaml:"configuration,omitempty"`
	Timeouts      Timeouts      `json:"timeouts" yaml:"timeouts"`
}

type Component struct {
	ID               string   `json:"id" yaml:"id"`
	Name             string   `json:"name" yaml:"name"`
	Version          string   `json:"version" yaml:"version"`
	SoftwareVersions []string `json:"softwareVersions" yaml:"softwareVersions"`
	Channel          string   `json:"channel" yaml:"channel"`
	Description      string   `json:"description,omitempty" yaml:"description,omitempty"`
}

type Compatibility struct {
	Panel         string   `json:"panel,omitempty" yaml:"panel,omitempty"`
	Systems       []System `json:"systems" yaml:"systems"`
	Architectures []string `json:"architectures" yaml:"architectures"`
}

type System struct {
	ID       string   `json:"id" yaml:"id"`
	Versions []string `json:"versions" yaml:"versions"`
}

type Dependencies struct {
	Components []ComponentDependency `json:"components,omitempty" yaml:"components,omitempty"`
	Packages   map[string][]string   `json:"packages,omitempty" yaml:"packages,omitempty"`
}

type ComponentDependency struct {
	ID         string `json:"id" yaml:"id"`
	Constraint string `json:"constraint,omitempty" yaml:"constraint,omitempty"`
	Optional   bool   `json:"optional,omitempty" yaml:"optional,omitempty"`
}

type Actions struct {
	Precheck    string `json:"precheck" yaml:"precheck"`
	Install     string `json:"install" yaml:"install"`
	Configure   string `json:"configure,omitempty" yaml:"configure,omitempty"`
	Verify      string `json:"verify" yaml:"verify"`
	Upgrade     string `json:"upgrade,omitempty" yaml:"upgrade,omitempty"`
	Rollback    string `json:"rollback,omitempty" yaml:"rollback,omitempty"`
	Uninstall   string `json:"uninstall" yaml:"uninstall"`
	Status      string `json:"status,omitempty" yaml:"status,omitempty"`
	Start       string `json:"start,omitempty" yaml:"start,omitempty"`
	Stop        string `json:"stop,omitempty" yaml:"stop,omitempty"`
	Restart     string `json:"restart,omitempty" yaml:"restart,omitempty"`
	Reload      string `json:"reload,omitempty" yaml:"reload,omitempty"`
	ConfigGet   string `json:"configGet,omitempty" yaml:"configGet,omitempty"`
	ConfigApply string `json:"configApply,omitempty" yaml:"configApply,omitempty"`
}

type Parameter struct {
	Name        string `json:"name" yaml:"name"`
	Type        string `json:"type" yaml:"type"`
	Required    bool   `json:"required,omitempty" yaml:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty" yaml:"secret,omitempty"`
	Default     string `json:"default,omitempty" yaml:"default,omitempty"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

type Configuration struct {
	ApplyMode string               `json:"applyMode,omitempty" yaml:"applyMode,omitempty"`
	Fields    []ConfigurationField `json:"fields,omitempty" yaml:"fields,omitempty"`
}

type ConfigurationField struct {
	Key         string   `json:"key" yaml:"key"`
	Label       string   `json:"label" yaml:"label"`
	Type        string   `json:"type" yaml:"type"`
	Env         string   `json:"env" yaml:"env"`
	Unit        string   `json:"unit,omitempty" yaml:"unit,omitempty"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Min         *int     `json:"min,omitempty" yaml:"min,omitempty"`
	Max         *int     `json:"max,omitempty" yaml:"max,omitempty"`
	Options     []string `json:"options,omitempty" yaml:"options,omitempty"`
}

type Timeouts struct {
	Precheck    int `json:"precheck" yaml:"precheck"`
	Install     int `json:"install" yaml:"install"`
	Configure   int `json:"configure,omitempty" yaml:"configure,omitempty"`
	Verify      int `json:"verify" yaml:"verify"`
	Upgrade     int `json:"upgrade,omitempty" yaml:"upgrade,omitempty"`
	Rollback    int `json:"rollback" yaml:"rollback"`
	Uninstall   int `json:"uninstall" yaml:"uninstall"`
	Status      int `json:"status,omitempty" yaml:"status,omitempty"`
	Start       int `json:"start,omitempty" yaml:"start,omitempty"`
	Stop        int `json:"stop,omitempty" yaml:"stop,omitempty"`
	Restart     int `json:"restart,omitempty" yaml:"restart,omitempty"`
	Reload      int `json:"reload,omitempty" yaml:"reload,omitempty"`
	ConfigGet   int `json:"configGet,omitempty" yaml:"configGet,omitempty"`
	ConfigApply int `json:"configApply,omitempty" yaml:"configApply,omitempty"`
}

type Host struct {
	PanelVersion  string `json:"panelVersion"`
	SystemID      string `json:"systemId"`
	SystemVersion string `json:"systemVersion"`
	Architecture  string `json:"architecture"`
}

type ResolveRequest struct {
	Component       string `json:"component"`
	SoftwareVersion string `json:"softwareVersion,omitempty"`
	Channel         string `json:"channel"`
	Host            Host   `json:"host"`
}

type Metadata struct {
	Manifest    Manifest   `json:"manifest"`
	SHA256      string     `json:"sha256"`
	Signature   string     `json:"signature"`
	KeyID       string     `json:"keyId"`
	Size        int64      `json:"size"`
	DownloadURL string     `json:"downloadUrl"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

type APIError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Package struct {
	Manifest Manifest
	Root     string
	Source   string
}

func parseManifest(contents []byte) (Manifest, error) {
	var result Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	if err := decoder.Decode(&result); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return Manifest{}, fmt.Errorf("decode manifest: multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := result.validate(); err != nil {
		return Manifest{}, err
	}
	return result, nil
}

func (m Manifest) validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported manifest schemaVersion %d", m.SchemaVersion)
	}
	if !componentIDPattern.MatchString(m.Component.ID) {
		return fmt.Errorf("invalid component id %q", m.Component.ID)
	}
	if !versionPattern.MatchString(m.Component.Version) {
		return fmt.Errorf("invalid component package version %q", m.Component.Version)
	}
	if len(m.Component.SoftwareVersions) == 0 {
		return fmt.Errorf("component softwareVersions is required")
	}
	for _, supported := range m.Component.SoftwareVersions {
		supported = strings.TrimSpace(supported)
		if supported == "" {
			return fmt.Errorf("component contains an empty software version")
		}
	}
	switch m.Component.Channel {
	case "stable", "beta", "development":
	default:
		return fmt.Errorf("invalid component channel %q", m.Component.Channel)
	}
	for name, action := range m.actionMap() {
		if action == "" {
			if name == "precheck" || name == "install" || name == "verify" || name == "uninstall" {
				return fmt.Errorf("required action %s is missing", name)
			}
			continue
		}
		if action == "." || path.IsAbs(action) || path.Clean(action) != action ||
			strings.HasPrefix(action, "../") || strings.Contains(action, "\\") {
			return fmt.Errorf("invalid %s action path", name)
		}
	}
	serviceActions := map[string]string{
		"status":  m.Actions.Status,
		"start":   m.Actions.Start,
		"stop":    m.Actions.Stop,
		"restart": m.Actions.Restart,
	}
	serviceActionCount := 0
	for _, action := range serviceActions {
		if action != "" {
			serviceActionCount++
		}
	}
	if (serviceActionCount != 0 && serviceActionCount != len(serviceActions)) ||
		(m.Actions.Reload != "" && serviceActionCount != len(serviceActions)) {
		return fmt.Errorf("service actions status, start, stop, and restart must be declared together")
	}
	if m.RuntimeGroup != "" && !runtimeGroupPattern.MatchString(m.RuntimeGroup) {
		return fmt.Errorf("invalid runtime group %q", m.RuntimeGroup)
	}
	if m.ServiceName != "" && !serviceNamePattern.MatchString(m.ServiceName) {
		return fmt.Errorf("invalid service name %q", m.ServiceName)
	}
	if m.ServiceName != "" && serviceActionCount == 0 {
		return fmt.Errorf("service name requires service actions")
	}
	if m.RuntimeGroup != "" && m.ServiceName == "" {
		return fmt.Errorf("runtime group requires a service name")
	}
	if (m.Actions.ConfigGet == "") != (m.Actions.ConfigApply == "") {
		return fmt.Errorf("configGet and configApply actions must be declared together")
	}
	for name, timeout := range map[string]int{
		"status":      m.Timeouts.Status,
		"start":       m.Timeouts.Start,
		"stop":        m.Timeouts.Stop,
		"restart":     m.Timeouts.Restart,
		"reload":      m.Timeouts.Reload,
		"configGet":   m.Timeouts.ConfigGet,
		"configApply": m.Timeouts.ConfigApply,
	} {
		action := m.actionMap()[name]
		if action == "" {
			continue
		}
		if timeout < 1 || timeout > 3600 {
			return fmt.Errorf("%s timeout must be between 1 and 3600 seconds", name)
		}
	}
	for _, parameter := range m.Parameters {
		if !parameterPattern.MatchString(parameter.Name) {
			return fmt.Errorf("invalid parameter name %q", parameter.Name)
		}
		switch parameter.Name {
		case "PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "BASH_ENV", "ENV", "SHELLOPTS", "IFS", "CDPATH":
			return fmt.Errorf("parameter name %s is reserved", parameter.Name)
		}
		switch parameter.Type {
		case "string", "integer", "boolean", "port", "password", "path":
		default:
			return fmt.Errorf("invalid parameter type %q", parameter.Type)
		}
		if parameter.Secret && parameter.Default != "" {
			return fmt.Errorf("secret parameter %s cannot have a default", parameter.Name)
		}
	}
	if len(m.Configuration.Fields) > 0 {
		if m.Actions.ConfigGet == "" || m.Actions.ConfigApply == "" {
			return fmt.Errorf("configuration fields require configGet and configApply actions")
		}
		switch m.Configuration.ApplyMode {
		case "reload", "restart":
		default:
			return fmt.Errorf("invalid configuration apply mode %q", m.Configuration.ApplyMode)
		}
		seen := make(map[string]struct{}, len(m.Configuration.Fields))
		for _, field := range m.Configuration.Fields {
			if !configurationKeyPattern.MatchString(field.Key) || strings.TrimSpace(field.Label) == "" {
				return fmt.Errorf("invalid configuration field %q", field.Key)
			}
			if _, exists := seen[field.Key]; exists {
				return fmt.Errorf("duplicate configuration field %q", field.Key)
			}
			seen[field.Key] = struct{}{}
			if field.Env != "" && !parameterPattern.MatchString(field.Env) {
				return fmt.Errorf("invalid configuration environment parameter %q", field.Env)
			}
			switch field.Type {
			case "string", "integer", "boolean", "select", "port", "path", "worker_processes":
			default:
				return fmt.Errorf("invalid configuration field type %q", field.Type)
			}
			if field.Min != nil && field.Max != nil && *field.Min > *field.Max {
				return fmt.Errorf("configuration field %s has an invalid range", field.Key)
			}
		}
	}
	return nil
}

func (m Manifest) actionMap() map[string]string {
	return map[string]string{
		"precheck":    m.Actions.Precheck,
		"install":     m.Actions.Install,
		"configure":   m.Actions.Configure,
		"verify":      m.Actions.Verify,
		"upgrade":     m.Actions.Upgrade,
		"rollback":    m.Actions.Rollback,
		"uninstall":   m.Actions.Uninstall,
		"status":      m.Actions.Status,
		"start":       m.Actions.Start,
		"stop":        m.Actions.Stop,
		"restart":     m.Actions.Restart,
		"reload":      m.Actions.Reload,
		"configGet":   m.Actions.ConfigGet,
		"configApply": m.Actions.ConfigApply,
	}
}

func (m Manifest) supportsSoftwareVersion(requested string) bool {
	return SupportsSoftwareVersion(m.Component.SoftwareVersions, requested)
}

func SupportsSoftwareVersion(supported []string, requested string) bool {
	requested = strings.TrimSpace(requested)
	for _, version := range supported {
		version = strings.TrimSpace(version)
		if version == "*" || version == requested {
			return true
		}
	}
	if !softwareVersionPattern.MatchString(requested) {
		return false
	}
	for _, version := range supported {
		version = strings.TrimSpace(version)
		if !softwareVersionLinePattern.MatchString(version) {
			continue
		}
		prefix := strings.TrimSuffix(version, ".x")
		requestedParts := strings.Split(strings.TrimPrefix(requested, "v"), ".")
		lineParts := strings.Split(strings.TrimPrefix(prefix, "v"), ".")
		if len(requestedParts) >= len(lineParts) && strings.Join(requestedParts[:len(lineParts)], ".") == strings.Join(lineParts, ".") {
			return true
		}
	}
	return false
}

func (p Package) Action(name string) (string, error) {
	action, exists := p.Manifest.actionMap()[name]
	if !exists || action == "" {
		return "", fmt.Errorf("component %s has no %s action", p.Manifest.Component.ID, name)
	}
	return path.Join(p.Root, action), nil
}
