package software

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"

	"gorm.io/gorm"
)

const (
	RecoveryInstalledHealthy    = "installed_healthy"
	RecoveryRunningUnrecorded   = "running_unrecorded"
	RecoveryInstalledUnhealthy  = "installed_unhealthy"
	RecoveryNotInstalled        = "not_installed"
	RecoveryVersionInconsistent = "version_inconsistent"
	RecoveryUnknown             = "unknown"
)

// InspectInstallRecovery performs a read-only database and process inspection.
// It never resumes a script or marks an interrupted task successful.
func InspectInstallRecovery(
	ctx context.Context,
	softwareKey string,
	component string,
	requestedVersion string,
) (string, string) {
	if app.DB() == nil {
		return RecoveryUnknown, "数据库不可用，无法核验组件状态"
	}
	var installed models.Software
	dbResult := app.DB().
		Where("`key` = ? AND installed = ?", softwareKey, true).
		Order("install_time DESC").
		First(&installed)
	dbInstalled := dbResult.Error == nil
	if dbResult.Error != nil && !errors.Is(dbResult.Error, gorm.ErrRecordNotFound) {
		return RecoveryUnknown, "读取组件安装记录失败"
	}

	running, err := recoveryProcessRunning(ctx, component)
	if err != nil {
		if dbInstalled {
			return RecoveryUnknown, "存在安装记录，但进程状态探测失败"
		}
		return RecoveryUnknown, "进程状态探测失败"
	}
	if running && !dbInstalled {
		return RecoveryRunningUnrecorded, "检测到组件进程正在运行，但安装记录尚未提交，请人工核验版本"
	}
	if !running && dbInstalled {
		return RecoveryInstalledUnhealthy, "存在安装记录，但组件进程未运行，请先检查服务状态与安装日志"
	}
	if !running {
		return RecoveryNotInstalled, "未检测到已安装记录或运行中的组件进程，可以检查日志后重新提交"
	}

	recordedVersion := strings.TrimSpace(installed.InstallVersion)
	if recordedVersion == "" {
		return RecoveryVersionInconsistent, "组件正在运行，但安装记录缺少版本信息，请人工核验"
	}
	if requested := strings.TrimSpace(requestedVersion); requested != "" && recordedVersion != requested {
		return RecoveryVersionInconsistent, fmt.Sprintf(
			"组件正在运行，但记录版本为 %s，与任务请求版本不一致",
			recordedVersion,
		)
	}
	return RecoveryInstalledHealthy, "组件进程正在运行且安装版本记录一致；任务仍保持中断状态，避免误判成功"
}

// InspectTaskRecovery applies operation-aware wording to the same read-only
// database/process probe. Interrupted uninstalls stay interrupted even when
// the component already appears absent because a partial removal still
// requires operator review.
func InspectTaskRecovery(
	ctx context.Context,
	operation string,
	softwareKey string,
	component string,
	requestedVersion string,
) (string, string) {
	status, message := InspectInstallRecovery(ctx, softwareKey, component, requestedVersion)
	operation = strings.ToLower(strings.TrimSpace(operation))
	if operation != "uninstall" && operation != "configure" && !IsServiceAction(operation) {
		return status, message
	}
	if operation == "configure" {
		switch status {
		case RecoveryInstalledHealthy:
			return status, "配置任务中断后组件当前正在运行；任务仍保持中断，请结合配置版本与日志确认发布结果"
		case RecoveryInstalledUnhealthy:
			return status, "配置任务中断后组件当前未运行；请检查配置语法、自动备份和服务日志"
		default:
			return status, "配置任务中断后无法确认最终结果；" + message
		}
	}
	if IsServiceAction(operation) {
		label := map[string]string{
			"start": "启动", "stop": "停止", "restart": "重启", "reload": "重载",
		}[operation]
		switch status {
		case RecoveryInstalledHealthy:
			return status, label + "任务中断后组件当前正在运行；任务仍保持中断，请结合日志确认动作结果"
		case RecoveryInstalledUnhealthy:
			return status, label + "任务中断后组件当前未运行；任务仍保持中断，请结合日志确认动作结果"
		default:
			return status, label + "任务中断后无法确认最终结果；" + message
		}
	}
	switch status {
	case RecoveryInstalledHealthy:
		return status, "卸载中断后组件仍有安装记录且进程正在运行；可检查日志后重新提交卸载"
	case RecoveryInstalledUnhealthy:
		return status, "卸载中断后仍有安装记录，但组件进程未运行；请先核验文件和服务状态"
	case RecoveryRunningUnrecorded:
		return status, "卸载中断后组件进程仍在运行，但安装记录缺失；请人工核验后再操作"
	case RecoveryNotInstalled:
		return status, "卸载中断后未检测到安装记录或运行进程；任务仍保持中断，请确认数据和文件状态"
	case RecoveryVersionInconsistent:
		return status, "卸载中断后的组件版本与任务请求不一致；请人工核验后再操作"
	default:
		return status, "卸载中断后无法确认组件状态；" + message
	}
}

func recoveryProcessRunning(ctx context.Context, component string) (bool, error) {
	var processNames []string
	switch strings.ToLower(strings.TrimSpace(component)) {
	case "nginx", "openresty", "tengine":
		processNames = []string{"nginx"}
	case "caddy":
		processNames = []string{"caddy"}
	case "apache":
		processNames = []string{"httpd"}
	case "mysql":
		processNames = []string{"mysqld", "mysqld_safe"}
	case "redis":
		processNames = []string{"redis-server"}
	case "php":
		processNames = []string{"php-fpm", "php-fpm8", "php-fpm7"}
	default:
		return false, fmt.Errorf("unsupported recovery probe component %q", component)
	}
	output, err := exec.CommandContext(ctx, "ps", "-eo", "comm=").Output()
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(output), "\n") {
		name := strings.ToLower(strings.TrimSpace(line))
		for _, expected := range processNames {
			if name == expected || strings.HasPrefix(name, expected) {
				return true, nil
			}
		}
	}
	return false, nil
}

func ComponentProcessRunning(ctx context.Context, component string) (bool, error) {
	return recoveryProcessRunning(ctx, component)
}
