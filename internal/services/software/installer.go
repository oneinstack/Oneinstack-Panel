package software

import (
	"fmt"
	"oneinstack/internal/services/script"
	"oneinstack/router/input"
)

// Installer 软件安装器
type Installer struct {
	scriptManager *script.ScriptManager
}

// NewInstaller 创建新的安装器
func NewInstaller() *Installer {
	return &Installer{
		scriptManager: script.NewScriptManager(),
	}
}

// Install 安装软件
func (installer *Installer) Install(params *input.InstallParams, async bool) (string, error) {
	// 获取安装脚本
	scriptInfo, err := installer.getInstallScript(params)
	if err != nil {
		return "", err
	}

	// 设置脚本参数
	installer.setScriptParams(scriptInfo, params)

	// 执行脚本
	return installer.scriptManager.ExecuteScript(scriptInfo, params, async)
}

// Uninstall 卸载软件
func (installer *Installer) Uninstall(params *input.RemoveParams, async bool) (string, error) {
	// 获取卸载脚本
	scriptInfo, err := installer.scriptManager.GetScript(script.ScriptTypeUninstall, params.Name)
	if err != nil {
		return "", err
	}

	// 执行脚本
	installParams := &input.InstallParams{
		Key:     params.Name,
		Version: params.Version,
	}

	return installer.scriptManager.ExecuteScript(scriptInfo, installParams, async)
}

// getInstallScript 获取安装脚本
func (installer *Installer) getInstallScript(params *input.InstallParams) (*script.ScriptInfo, error) {
	var scriptName string

	switch params.Key {
	case "webserver":
		scriptName = "nginx"
	case "db":
		switch params.Version {
		case "5.5":
			scriptName = "mysql55"
		case "5.7":
			scriptName = "mysql57"
		case "8.0":
			scriptName = "mysql80"
		default:
			return nil, fmt.Errorf("unsupported MySQL version: %s", params.Version)
		}
	case "redis":
		scriptName = "redis"
	case "php":
		scriptName = "php"
	case "java":
		switch params.Version {
		case "11":
			scriptName = "openjdk11"
		case "17":
			scriptName = "openjdk17"
		case "18":
			scriptName = "openjdk18"
		default:
			return nil, fmt.Errorf("unsupported Java version: %s", params.Version)
		}
	case "openresty":
		scriptName = "openresty"
	case "phpmyadmin":
		scriptName = "phpmyadmin"
	default:
		return nil, fmt.Errorf("unsupported software: %s", params.Key)
	}

	return installer.scriptManager.GetScript(script.ScriptTypeInstall, scriptName)
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
	case "php":
		scriptInfo.Params["PHP_VERSION"] = params.Version
	case "java":
		scriptInfo.Params["JAVA_VERSION"] = params.Version
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
