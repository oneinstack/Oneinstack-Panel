package system

import (
	"os"
	"runtime"

	"oneinstack/internal/i18n"
)

const (
	RuntimeModeRoot    = "root"
	RuntimeModeNonRoot = "non-root"
	RuntimeModeUnknown = "unknown"

	RuntimeWarningPanelNonRoot = "PANEL_NOT_RUNNING_AS_ROOT"
)

type RuntimeWarning struct {
	Code           string   `json:"code"`
	Level          string   `json:"level"`
	Title          string   `json:"title"`
	Message        string   `json:"message"`
	Detail         string   `json:"detail"`
	AffectedScopes []string `json:"affectedScopes"`
}

type RuntimeStatus struct {
	Platform      string          `json:"platform"`
	Mode          string          `json:"mode"`
	RunningAsRoot *bool           `json:"runningAsRoot"`
	Warning       *RuntimeWarning `json:"warning"`
}

func GetRuntimeStatus(locale string) RuntimeStatus {
	status := RuntimeStatus{
		Platform: runtime.GOOS,
		Mode:     RuntimeModeUnknown,
	}
	if runtime.GOOS != "linux" {
		return status
	}

	runningAsRoot := os.Geteuid() == 0
	status.RunningAsRoot = &runningAsRoot
	if runningAsRoot {
		status.Mode = RuntimeModeRoot
		return status
	}

	status.Mode = RuntimeModeNonRoot
	status.Warning = &RuntimeWarning{
		Code:    RuntimeWarningPanelNonRoot,
		Level:   "warning",
		Title:   i18n.Message(locale, i18n.MessagePanelNonRootTitle, "Panel 未以 root 用户运行"),
		Message: i18n.Message(locale, i18n.MessagePanelNonRootMessage, "当前 Panel 可以继续使用，但部分系统级功能可能不可用。"),
		Detail:  i18n.Message(locale, i18n.MessagePanelNonRootDetail, "组件安装和维护、系统服务控制、Panel 更新、备份恢复以及网络配置通常需要 Linux root 权限。"),
		AffectedScopes: []string{
			"software.lifecycle",
			"system.service",
			"panel.update",
			"backup.restore",
			"network.write",
		},
	}
	return status
}
