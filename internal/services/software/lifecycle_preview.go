package software

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
)

// LifecyclePreview is resolved exclusively from a verified component package
// and installed-state parameters. It contains no executable client input.
type LifecyclePreview struct {
	Contract        bool
	Component       string
	DisplayName     string
	SoftwareVersion string
	PackagePin      *scriptregistry.PackagePin
	PackageManager  string
	Packages        []string
	Services        []string
	Paths           []LifecyclePreviewPath
	Rollback        scriptregistry.PreviewRollback
}

type LifecyclePreviewPath struct {
	Path   string
	Role   string
	Action string
}

// PreviewUninstallLifecycle resolves the same uninstall package and effective
// installed parameters that execution will use. A supplied pin always uses
// ResolveFixed, preventing a newer package from replacing the reviewed one.
func PreviewUninstallLifecycle(ctx context.Context, params *input.RemoveParams) (LifecyclePreview, error) {
	if params == nil {
		return LifecyclePreview{}, errors.New("uninstall parameters are required")
	}
	component, softwareKey, err := componentForRemove(params.Name)
	if err != nil {
		return LifecyclePreview{}, err
	}
	version := strings.TrimSpace(params.Version)
	if version == "" {
		return LifecyclePreview{}, errors.New("uninstall software version is required")
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return LifecyclePreview{}, err
	}
	pkg, err := resolveLifecyclePackage(ctx, registry, component, version, "uninstall", params.ResolvedPackage)
	if err != nil {
		return LifecyclePreview{}, fmt.Errorf("resolve %s uninstall package: %w", component, err)
	}
	scriptInfo, err := scriptInfoFromPackage(pkg, "uninstall", version)
	if err != nil {
		return LifecyclePreview{}, err
	}
	installParams := &input.InstallParams{
		Key:        softwareKey,
		Version:    version,
		Parameters: persistedUninstallParameters(softwareKey, params),
	}
	if strings.TrimSpace(params.DataPolicy) != "" {
		installParams.Parameters["data-policy"] = strings.TrimSpace(params.DataPolicy)
	}
	if params.ConfirmDataDeletion {
		installParams.Parameters["delete-data-confirm"] = "true"
	}
	NewInstaller().setScriptParams(scriptInfo, installParams)
	if err := script.ValidateParameters(scriptInfo); err != nil {
		return LifecyclePreview{}, fmt.Errorf("validate uninstall preview parameters: %w", err)
	}
	return lifecyclePreviewFromPackage(ctx, pkg, scriptInfo.PackagePin, version, "uninstall", params.DataPolicy, scriptInfo.Params)
}

// PreviewServiceLifecycle resolves an installed component version and its
// concrete service units from the signed package preview contract.
func PreviewServiceLifecycle(
	ctx context.Context,
	component string,
	action string,
	pin *scriptregistry.PackagePin,
) (LifecyclePreview, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	definition, err := ResolveServiceComponent(app.DB(), component)
	if err != nil {
		return LifecyclePreview{}, err
	}
	if !IsServiceAction(action) {
		return LifecyclePreview{}, fmt.Errorf("unsupported service action: %s", action)
	}
	var row models.Software
	if app.DB() == nil {
		return LifecyclePreview{}, errors.New("software database is unavailable")
	}
	if err := app.DB().Where("installed = ? AND (`key` = ? OR component = ?)", true, definition.SoftwareKey, definition.Component).
		Order("install_time DESC, id DESC").First(&row).Error; err != nil {
		return LifecyclePreview{}, fmt.Errorf("read installed %s state: %w", definition.Component, err)
	}
	version := strings.TrimSpace(row.InstallVersion)
	if version == "" {
		version = strings.TrimSpace(row.Version)
	}
	if version == "" {
		return LifecyclePreview{}, fmt.Errorf("installed %s version is missing", definition.Component)
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return LifecyclePreview{}, err
	}
	pkg, err := resolveLifecyclePackage(ctx, registry, definition.Component, version, action, pin)
	if err != nil {
		return LifecyclePreview{}, fmt.Errorf("resolve %s %s package: %w", definition.Component, action, err)
	}
	scriptInfo, err := scriptInfoFromPackage(pkg, action, version)
	if err != nil {
		return LifecyclePreview{}, err
	}
	return lifecyclePreviewFromPackage(ctx, pkg, scriptInfo.PackagePin, version, action, "preserve", nil)
}

func resolveLifecyclePackage(
	ctx context.Context,
	registry *scriptregistry.Registry,
	component string,
	version string,
	action string,
	pin *scriptregistry.PackagePin,
) (scriptregistry.Package, error) {
	if pin != nil {
		pkg, err := registry.ResolveFixed(component, version, *pin)
		if err != nil {
			return scriptregistry.Package{}, err
		}
		if _, err := pkg.Action(action); err != nil {
			return scriptregistry.Package{}, err
		}
		return pkg, nil
	}
	return registry.ResolveInstalled(ctx, component, version, action)
}

func lifecyclePreviewFromPackage(
	ctx context.Context,
	pkg scriptregistry.Package,
	pin *scriptregistry.PackagePin,
	softwareVersion string,
	action string,
	dataPolicy string,
	parameters map[string]string,
) (LifecyclePreview, error) {
	manifest := pkg.Manifest
	result := LifecyclePreview{
		Component:       manifest.Component.ID,
		DisplayName:     manifest.Component.Name,
		SoftwareVersion: softwareVersion,
		PackagePin:      pin,
	}
	if manifest.Preview == nil {
		return result, nil
	}
	result.Contract = true
	if pin == nil || strings.TrimSpace(pin.PackageSHA256) == "" {
		return LifecyclePreview{}, errors.New("lifecycle preview package is not immutably pinned")
	}
	rollback, exists := manifest.Preview.Rollback[action]
	if !exists {
		return LifecyclePreview{}, fmt.Errorf("lifecycle preview rollback metadata is missing for %s", action)
	}
	result.Rollback = rollback
	for _, service := range manifest.Preview.Services {
		if containsPreviewValue(service.Actions, action) {
			result.Services = append(result.Services, service.Name)
		}
	}
	if action != "uninstall" && len(result.Services) == 0 {
		return LifecyclePreview{}, fmt.Errorf("lifecycle preview service metadata is missing for %s", action)
	}
	result.PackageManager = lifecyclePackageManager(pin.TargetOS, pin.TargetOSVersion)
	if action == "uninstall" {
		packageMetadataRequired := false
		for _, set := range manifest.Preview.Packages {
			if previewVersionMatches(set.SoftwareVersions, softwareVersion) {
				packageMetadataRequired = true
				break
			}
		}
		set, packageMetadataFound := manifest.Preview.Packages[result.PackageManager]
		if packageMetadataRequired && (result.PackageManager == "" || !packageMetadataFound || !previewVersionMatches(set.SoftwareVersions, softwareVersion)) {
			return LifecyclePreview{}, fmt.Errorf("lifecycle preview package metadata is missing for target system %s", pin.TargetOS)
		}
		if packageMetadataFound && previewVersionMatches(set.SoftwareVersions, softwareVersion) {
			result.Packages = append(result.Packages, set.Names...)
			if len(set.InstalledAlternatives) > 0 {
				installed, err := installedPreviewAlternative(ctx, result.PackageManager, set.InstalledAlternatives)
				if err != nil {
					return LifecyclePreview{}, err
				}
				result.Packages = append(result.Packages, installed)
			}
		}
	}
	if action == "uninstall" {
		policy := strings.ToLower(strings.TrimSpace(dataPolicy))
		if policy == "" {
			policy = "preserve"
		}
		for _, target := range manifest.Preview.Paths {
			if !previewVersionMatches(target.SoftwareVersions, softwareVersion) {
				continue
			}
			value := strings.TrimSpace(target.Path)
			if target.Parameter != "" {
				env := previewParameterEnvironment(manifest.Parameters, target.Parameter)
				value = strings.TrimSpace(parameters[env])
			}
			if value == "" {
				return LifecyclePreview{}, fmt.Errorf("lifecycle preview path %s has no effective value", target.Parameter)
			}
			pathAction := target.PreserveAction
			if policy == "delete" {
				pathAction = target.DeleteAction
			}
			if err := validateLifecyclePreviewPath(value, pathAction); err != nil {
				return LifecyclePreview{}, err
			}
			result.Paths = append(result.Paths, LifecyclePreviewPath{Path: value, Role: target.Role, Action: pathAction})
		}
	}
	return result, nil
}

func validateLifecyclePreviewPath(value, action string) error {
	cleaned := filepath.Clean(value)
	if !filepath.IsAbs(value) || cleaned != value || cleaned == string(filepath.Separator) {
		return fmt.Errorf("lifecycle preview path %q is not a normalized absolute path", value)
	}
	if action == "preserve" || action == "remove_managed" {
		return nil
	}
	switch cleaned {
	case "/bin", "/boot", "/data", "/dev", "/etc", "/home", "/lib", "/lib64", "/opt", "/proc", "/root", "/run", "/sbin", "/srv", "/sys", "/tmp", "/usr", "/usr/local", "/var", "/var/lib", "/var/log":
		return fmt.Errorf("lifecycle preview refuses broad destructive path %q", value)
	default:
		return nil
	}
}

func lifecyclePackageManager(systemID, systemVersion string) string {
	switch strings.ToLower(strings.TrimSpace(systemID)) {
	case "ubuntu", "debian":
		return "apt"
	case "centos":
		if strings.HasPrefix(strings.TrimSpace(systemVersion), "7") {
			return "yum"
		}
		return "dnf"
	case "rhel", "rocky", "almalinux", "ol", "fedora", "amzn", "opencloudos", "anolis", "alinux", "euleros", "openeuler":
		return "dnf"
	case "sles", "opensuse", "opensuse-leap", "opensuse-tumbleweed":
		return "zypper"
	default:
		return ""
	}
}

func installedPreviewAlternative(ctx context.Context, manager string, alternatives []string) (string, error) {
	for _, name := range alternatives {
		var command *exec.Cmd
		if manager == "apt" {
			command = exec.CommandContext(ctx, "dpkg-query", "-W", "-f=${Status}", name)
		} else {
			command = exec.CommandContext(ctx, "rpm", "-q", name)
		}
		if err := command.Run(); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("none of the declared lifecycle preview packages are installed: %s", strings.Join(alternatives, ", "))
}

func previewVersionMatches(versions []string, softwareVersion string) bool {
	return len(versions) == 0 || scriptregistry.SupportsSoftwareVersion(versions, softwareVersion)
}

func containsPreviewValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func previewParameterEnvironment(parameters []scriptregistry.Parameter, name string) string {
	for _, parameter := range parameters {
		if parameter.Name != name {
			continue
		}
		if strings.TrimSpace(parameter.Env) != "" {
			return strings.TrimSpace(parameter.Env)
		}
		return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(parameter.Name))
	}
	return ""
}
