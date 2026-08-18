package software

import (
	"context"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	safeservice "oneinstack/internal/services/safe"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
	"path/filepath"
	"strings"
	"time"
)

var bundledOnlySoftwareKeys = map[string]struct{}{
	"db":        {},
	"redis":     {},
	"webserver": {},
	"php":       {},
	"firewalld": {},
}

// Installer 软件安装器
type Installer struct {
	scriptManager *script.ScriptManager
}

type packageResolutionObserver interface {
	OnPackageResolved(version, source string)
}

func reportPackageResolution(observer script.ExecutionObserver, info *script.ScriptInfo) {
	if observer == nil || info == nil {
		return
	}
	if reporter, ok := observer.(packageResolutionObserver); ok {
		reporter.OnPackageResolved(info.PackageVersion, info.Source)
	}
}

// NewInstaller 创建新的安装器
func NewInstaller() *Installer {
	return &Installer{
		scriptManager: script.NewScriptManager(),
	}
}

// Install 安装软件
func (installer *Installer) Install(params *input.InstallParams, async bool) (string, error) {
	return installer.install(context.Background(), params, async)
}

func (installer *Installer) install(ctx context.Context, params *input.InstallParams, async bool) (string, error) {
	actionName := "install"
	if app.DB() != nil {
		var installed int64
		app.DB().Model(&models.Software{}).
			Where("`key` = ? AND installed = ?", params.Key, true).
			Count(&installed)
		if installed > 0 {
			actionName = "upgrade"
		}
	}
	scriptInfo, err := installer.getInstallScript(ctx, params, actionName)
	if err != nil {
		return "", err
	}

	// 设置脚本参数
	installer.setScriptParams(scriptInfo, params)

	// 执行脚本
	return installer.scriptManager.ExecuteScript(scriptInfo, params, async)
}

// InstallTask executes an installation synchronously under the durable task
// runner. Progress is emitted at action boundaries and through the component
// script's optional FD 3 JSON stream.
func (installer *Installer) InstallTask(
	ctx context.Context,
	params *input.InstallParams,
	logPath string,
	observer script.ExecutionObserver,
) (string, error) {
	actionName := "install"
	if app.DB() != nil {
		var installed int64
		if err := app.DB().Model(&models.Software{}).
			Where("`key` = ? AND installed = ?", params.Key, true).
			Count(&installed).Error; err != nil {
			return "", err
		}
		if installed > 0 {
			actionName = "upgrade"
		}
	}
	scriptInfo, err := installer.getInstallScript(ctx, params, actionName)
	if err != nil {
		return "", err
	}
	reportPackageResolution(observer, scriptInfo)
	installer.setScriptParams(scriptInfo, params)
	return installer.scriptManager.ExecuteScriptTask(ctx, scriptInfo, params, logPath, observer)
}

// Uninstall 卸载软件
func (installer *Installer) Uninstall(params *input.RemoveParams, async bool) (string, error) {
	scriptInfo, installParams, err := installer.getUninstallScript(context.Background(), params)
	if err != nil {
		return "", err
	}
	logName, err := installer.scriptManager.ExecuteScript(scriptInfo, installParams, async)
	if err != nil || async {
		return logName, err
	}
	if err := cleanupUninstalledBackend(installParams.Key); err != nil {
		return logName, fmt.Errorf("清理卸载后的防火墙保护记录失败: %w", err)
	}
	return logName, nil
}

// UninstallTask executes a component uninstall under the durable task runner.
// Component uninstall scripts preserve application data by default; a failed
// or canceled action therefore leaves the installed database flag unchanged.
func (installer *Installer) UninstallTask(
	ctx context.Context,
	params *input.RemoveParams,
	logPath string,
	observer script.ExecutionObserver,
) (string, error) {
	scriptInfo, installParams, err := installer.getUninstallScript(ctx, params)
	if err != nil {
		return "", err
	}
	reportPackageResolution(observer, scriptInfo)
	logName, err := installer.scriptManager.ExecuteScriptTask(
		ctx,
		scriptInfo,
		installParams,
		logPath,
		observer,
	)
	if err != nil {
		return logName, err
	}
	if err := cleanupUninstalledBackend(installParams.Key); err != nil {
		return logName, fmt.Errorf("清理卸载后的防火墙保护记录失败: %w", err)
	}
	return logName, nil
}

func cleanupUninstalledBackend(softwareKey string) error {
	if strings.ToLower(strings.TrimSpace(softwareKey)) != safeservice.BackendFirewalld {
		return nil
	}
	return safeservice.NewDefaultService().CleanupUninstalledBackend(safeservice.BackendFirewalld)
}

// ServiceActionTask executes a fixed service lifecycle action from the
// verified component package. It never changes the software installation row.
func (installer *Installer) ServiceActionTask(
	ctx context.Context,
	component string,
	version string,
	action string,
	logPath string,
	observer script.ExecutionObserver,
) (string, error) {
	definition, err := ResolveServiceComponent(app.DB(), component)
	if err != nil {
		return "", err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if !IsServiceAction(action) {
		return "", fmt.Errorf("unsupported service action: %s", action)
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return "", err
	}
	componentPackage, err := registry.ResolveInstalled(
		ctx,
		definition.Component,
		strings.TrimSpace(version),
		action,
	)
	if err != nil {
		return "", fmt.Errorf("resolve %s %s package: %w", definition.Component, action, err)
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, action)
	if err != nil {
		return "", err
	}
	reportPackageResolution(observer, scriptInfo)
	params := (&serviceInstallParams{
		key:     definition.SoftwareKey,
		version: strings.TrimSpace(version),
	}).input()
	installer.setScriptParams(scriptInfo, params)
	return installer.scriptManager.ExecuteScriptTask(ctx, scriptInfo, params, logPath, observer)
}

func (installer *Installer) getUninstallScript(
	ctx context.Context,
	params *input.RemoveParams,
) (*script.ScriptInfo, *input.InstallParams, error) {
	componentName, softwareKey, err := componentForRemove(params.Name)
	if err != nil {
		return nil, nil, err
	}
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return nil, nil, err
	}
	componentPackage, err := registry.ResolveInstalledUninstall(
		ctx,
		componentName,
		params.Version,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve %s uninstall package: %w", componentName, err)
	}
	scriptInfo, err := scriptInfoFromPackage(componentPackage, "uninstall")
	if err != nil {
		return nil, nil, err
	}
	installParams := &input.InstallParams{
		Key:        softwareKey,
		Version:    params.Version,
		Parameters: params.Parameters,
	}
	installer.setScriptParams(scriptInfo, installParams)
	return scriptInfo, installParams, nil
}

// getInstallScript 获取安装脚本
func (installer *Installer) getInstallScript(ctx context.Context, params *input.InstallParams, actionName string) (*script.ScriptInfo, error) {
	var componentName string
	var legacyScriptName string
	var catalogManaged bool
	var catalogChannel string
	if app.DB() != nil {
		var catalogRow models.Software
		if err := app.DB().
			Where("`key` = ? AND version = ? AND component <> ''", params.Key, params.Version).
			First(&catalogRow).Error; err == nil {
			componentName = strings.ToLower(strings.TrimSpace(catalogRow.Component))
			catalogManaged = catalogRow.CatalogManaged
			catalogChannel = strings.ToLower(strings.TrimSpace(catalogRow.CatalogChannel))
		}
	}

	switch params.Key {
	case "webserver":
		if componentName == "" {
			componentName = "nginx"
		}
		legacyScriptName = "nginx"
	case "db":
		if componentName == "" {
			componentName = "mysql"
		}
		switch params.Version {
		case "5.5":
			legacyScriptName = "mysql55"
		case "5.7":
			legacyScriptName = "mysql57"
		case "8.0":
			legacyScriptName = "mysql80"
		default:
			if componentName == "" {
				return nil, fmt.Errorf("unsupported MySQL version: %s", params.Version)
			}
		}
	case "redis":
		if componentName == "" {
			componentName = "redis"
		}
		legacyScriptName = "redis"
	case "php":
		if componentName == "" {
			componentName = "php"
		}
		legacyScriptName = "php"
	case "java":
		if componentName == "" {
			componentName = "java"
		}
		switch params.Version {
		case "11":
			legacyScriptName = "openjdk11"
		case "17":
			legacyScriptName = "openjdk17"
		case "18":
			legacyScriptName = "openjdk18"
		default:
			if componentName == "" {
				return nil, fmt.Errorf("unsupported Java version: %s", params.Version)
			}
		}
	case "openresty":
		if componentName == "" {
			componentName = "openresty"
		}
		legacyScriptName = "openresty"
	case "phpmyadmin":
		if componentName == "" {
			componentName = "phpmyadmin"
		}
		legacyScriptName = "phpmyadmin"
	case "firewalld":
		if componentName == "" {
			componentName = "firewalld"
		}
	default:
		if componentName == "" {
			return nil, fmt.Errorf("unsupported software: %s", params.Key)
		}
	}

	registry, registryErr := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if registryErr == nil {
		componentPackage, resolveErr := registry.ResolveChannel(
			ctx,
			componentName,
			params.Version,
			catalogChannel,
		)
		if resolveErr == nil {
			if actionName == "upgrade" && componentPackage.Manifest.Actions.Upgrade == "" {
				actionName = "install"
			}
			return scriptInfoFromPackage(componentPackage, actionName)
		}
		registryErr = resolveErr
	}

	// A Center catalog entry is a promise that the matching signed component
	// package exists. Falling back to a legacy installer here can install a
	// different runtime version while the database records the requested
	// catalog version, leaving every subsequent lifecycle action unresolvable.
	if catalogManaged {
		return nil, fmt.Errorf(
			"resolve %s %s package: %w",
			componentName,
			actionName,
			registryErr,
		)
	}
	if _, bundledOnly := bundledOnlySoftwareKeys[params.Key]; bundledOnly {
		return nil, fmt.Errorf(
			"resolve %s %s package: %v",
			componentName,
			actionName,
			registryErr,
		)
	}
	if legacyScriptName == "" {
		return nil, fmt.Errorf("resolve %s installer: %v", componentName, registryErr)
	}
	legacyScript, fileErr := installer.scriptManager.GetScript(script.ScriptTypeInstall, legacyScriptName)
	if fileErr == nil {
		legacyScript.Source = "legacy-file"
		return legacyScript, nil
	}
	if content, exists := bundledLegacyScript(legacyScriptName); exists {
		return &script.ScriptInfo{
			Name:       legacyScriptName,
			Type:       script.ScriptTypeInstall,
			Content:    content,
			Params:     make(map[string]string),
			Source:     "legacy-embedded",
			ActionName: "install",
		}, nil
	}
	return nil, fmt.Errorf("resolve %s installer: %v; legacy fallback: %w", componentName, registryErr, fileErr)
}

func scriptInfoFromPackage(componentPackage scriptregistry.Package, actionName string) (*script.ScriptInfo, error) {
	actionPath, err := componentPackage.Action(actionName)
	if err != nil {
		return nil, err
	}
	manifest := componentPackage.Manifest
	result := &script.ScriptInfo{
		Name:           manifest.Component.ID,
		Type:           script.ScriptType(actionName),
		Path:           actionPath,
		WorkingDir:     componentPackage.Root,
		Source:         componentPackage.Source,
		PackageVersion: manifest.Component.Version,
		Params:         make(map[string]string),
		ActionName:     actionName,
		Timeouts: map[string]time.Duration{
			"precheck":    time.Duration(manifest.Timeouts.Precheck) * time.Second,
			"install":     time.Duration(manifest.Timeouts.Install) * time.Second,
			"configure":   time.Duration(manifest.Timeouts.Configure) * time.Second,
			"verify":      time.Duration(manifest.Timeouts.Verify) * time.Second,
			"upgrade":     time.Duration(manifest.Timeouts.Upgrade) * time.Second,
			"rollback":    time.Duration(manifest.Timeouts.Rollback) * time.Second,
			"uninstall":   time.Duration(manifest.Timeouts.Uninstall) * time.Second,
			"status":      time.Duration(manifest.Timeouts.Status) * time.Second,
			"start":       time.Duration(manifest.Timeouts.Start) * time.Second,
			"stop":        time.Duration(manifest.Timeouts.Stop) * time.Second,
			"restart":     time.Duration(manifest.Timeouts.Restart) * time.Second,
			"reload":      time.Duration(manifest.Timeouts.Reload) * time.Second,
			"configGet":   time.Duration(manifest.Timeouts.ConfigGet) * time.Second,
			"configApply": time.Duration(manifest.Timeouts.ConfigApply) * time.Second,
		},
	}
	for _, parameter := range manifest.Parameters {
		result.ParameterSpecs = append(result.ParameterSpecs, script.ParameterSpec{
			Name:     parameter.Name,
			Type:     parameter.Type,
			Required: parameter.Required,
			Secret:   parameter.Secret,
			Default:  parameter.Default,
		})
		if parameter.Default != "" {
			result.Params[parameter.Name] = parameter.Default
		}
	}
	if actionName != "uninstall" && !IsServiceAction(actionName) &&
		actionName != "status" && actionName != "configGet" &&
		actionName != "configApply" {
		result.PrecheckPath = optionalActionPath(componentPackage.Root, manifest.Actions.Precheck)
		result.ConfigurePath = optionalActionPath(componentPackage.Root, manifest.Actions.Configure)
		result.VerifyPath = optionalActionPath(componentPackage.Root, manifest.Actions.Verify)
		result.RollbackPath = optionalActionPath(componentPackage.Root, manifest.Actions.Rollback)
	}
	return result, nil
}

type serviceInstallParams struct {
	key     string
	version string
}

func (params *serviceInstallParams) input() *input.InstallParams {
	return &input.InstallParams{Key: params.key, Version: params.version}
}

func optionalActionPath(root, relative string) string {
	if strings.TrimSpace(relative) == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}

func bundledLegacyScript(name string) (string, bool) {
	scripts := map[string]string{
		"mysql55":    mysql55,
		"mysql57":    mysql57,
		"mysql80":    mysql80,
		"redis":      redis,
		"nginx":      nginx,
		"php":        php,
		"phpmyadmin": phpmyadmin,
		"openjdk11":  openJDK11,
		"openjdk17":  openJDK17,
		"openjdk18":  openJDK18,
		"openresty":  openresty,
	}
	content, exists := scripts[name]
	return content, exists
}

func componentForRemove(value string) (component string, softwareKey string, err error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if app.DB() != nil {
		var catalogRow models.Software
		result := app.DB().
			Where("(`key` = ? OR component = ?) AND component <> ''", normalized, normalized).
			Order("installed DESC, catalog_managed DESC, id DESC").
			First(&catalogRow)
		if result.Error == nil {
			return strings.ToLower(strings.TrimSpace(catalogRow.Component)), catalogRow.Key, nil
		}
	}
	switch normalized {
	case "nginx", "webserver":
		return "nginx", "webserver", nil
	case "mysql", "db":
		return "mysql", "db", nil
	case "redis":
		return "redis", "redis", nil
	case "php":
		return "php", "php", nil
	case "firewalld":
		return "firewalld", "firewalld", nil
	default:
		return "", "", fmt.Errorf("unsupported software for uninstall: %s", value)
	}
}

// setScriptParams 设置脚本参数
func (installer *Installer) setScriptParams(scriptInfo *script.ScriptInfo, params *input.InstallParams) {
	scriptInfo.Version = params.Version

	// 根据不同软件设置不同参数
	switch params.Key {
	case "db":
		if params.Pwd != "" {
			scriptInfo.Params["MYSQL_PASSWORD"] = params.Pwd
		}
		if params.Port != "" {
			scriptInfo.Params["MYSQL_PORT"] = params.Port
		}
	case "redis":
		if params.Port != "" {
			scriptInfo.Params["REDIS_PORT"] = params.Port
		}
		if params.Pwd != "" {
			scriptInfo.Params["REDIS_PASSWORD"] = params.Pwd
		}
	case "php":
		scriptInfo.Params["PHP_VERSION"] = params.Version
	case "java":
		scriptInfo.Params["JAVA_VERSION"] = params.Version
	}

	// Center can publish new database components without adding a hard-coded
	// software key to Panel. Map the generic password field to the package's
	// declared secret password parameter so MariaDB, Percona, PostgreSQL, and
	// MongoDB packages use the same safe installation pipeline as MySQL.
	if params.Pwd != "" {
		for _, parameter := range scriptInfo.ParameterSpecs {
			if !parameter.Secret || parameter.Type != "password" {
				continue
			}
			name := strings.ToUpper(strings.TrimSpace(parameter.Name))
			if name != "PASSWORD" && !strings.HasSuffix(name, "_PASSWORD") {
				continue
			}
			if _, exists := scriptInfo.Params[parameter.Name]; !exists {
				scriptInfo.Params[parameter.Name] = params.Pwd
			}
		}
	}
	// Component-specific install fields are accepted only when declared by the
	// resolved signed manifest. This supports new Center components without
	// hard-coding their parameter names while preventing arbitrary environment
	// variable injection.
	for _, parameter := range scriptInfo.ParameterSpecs {
		for key, value := range params.Parameters {
			parameterKey := strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(key))
			manifestKey := strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(parameter.Name))
			if parameterKey == manifestKey && value != "" {
				scriptInfo.Params[parameter.Name] = value
				break
			}
		}
	}

	// 通用参数
	scriptInfo.Params["SOFTWARE_VERSION"] = params.Version
}

// ListAvailableScripts 列出可用的脚本
func (installer *Installer) ListAvailableScripts(scriptType script.ScriptType) ([]string, error) {
	return installer.scriptManager.ListScripts(scriptType)
}

// CleanupOldFiles 清理旧文件
func (installer *Installer) CleanupOldFiles() error {
	// 清理1小时前的临时文件
	return installer.scriptManager.CleanupTempFiles(1 * 60 * 60 * 1000000000) // 1 hour in nanoseconds
}
