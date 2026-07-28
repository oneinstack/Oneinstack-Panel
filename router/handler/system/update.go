package system

import (
	"errors"
	"fmt"

	"github.com/gin-gonic/gin"

	"oneinstack/core"
	"oneinstack/internal/services/panelupdate"
	"oneinstack/router/input"
)

const panelUpdateConfirmation = "UPDATE PANEL"

func CheckPanelUpdate(c *gin.Context) {
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	recoveryNeeded, err := manager.NeedsRecovery()
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	if recoveryNeeded {
		handlePanelUpdateError(c, panelupdate.ErrRecoveryNeeded)
		return
	}
	result, err := manager.Check(c.Request.Context())
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func GetPanelUpdateStatus(c *gin.Context) {
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	status, err := manager.Status()
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	core.HandleSuccess(c, status)
}

func ApplyPanelUpdate(c *gin.Context) {
	var request input.ApplyPanelUpdateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if request.Confirm != panelUpdateConfirmation {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "确认文本必须为 "+panelUpdateConfirmation))
		return
	}
	manager, err := panelupdate.NewApplicationManager("")
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	recoveryNeeded, err := manager.NeedsRecovery()
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	if recoveryNeeded {
		handlePanelUpdateError(c, panelupdate.ErrRecoveryNeeded)
		return
	}
	result, err := manager.Check(c.Request.Context())
	if err != nil {
		handlePanelUpdateError(c, err)
		return
	}
	if !result.UpdateAvailable {
		handlePanelUpdateError(c, panelupdate.ErrNoUpdate)
		return
	}

	runner := panelupdate.OSCommandRunner{}
	if _, err := runner.Run(c.Request.Context(), panelupdate.Command{
		Name: "systemctl", Args: []string{"is-active", "--quiet", "one-update.service"},
	}); err == nil {
		handlePanelUpdateError(c, panelupdate.ErrUpdateBusy)
		return
	}
	if _, err := runner.Run(c.Request.Context(), panelupdate.Command{
		Name: "systemctl", Args: []string{"start", "--no-block", "one-update.service"},
	}); err != nil {
		handlePanelUpdateError(c, fmt.Errorf("启动独立更新服务: %w", err))
		return
	}
	core.HandleSuccess(c, gin.H{
		"accepted": true,
		"version":  result.LatestVersion,
		"message":  "更新任务已交给独立 systemd 单元执行，面板将在完成或回滚后恢复服务",
	})
}

func handlePanelUpdateError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, panelupdate.ErrDisabled):
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "面板更新中心未启用，请先在配置中开启更新中心"))
	case errors.Is(err, panelupdate.ErrInvalidManifest):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "更新清单校验失败"))
	case errors.Is(err, panelupdate.ErrNoUpdate):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "当前没有可安装的新版本"))
	case errors.Is(err, panelupdate.ErrUpdateBusy):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "已有面板更新正在执行"))
	case errors.Is(err, panelupdate.ErrRecoveryNeeded):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "检测到中断的更新事务，请先执行 one update rollback --yes"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "面板更新操作失败"))
	}
}
