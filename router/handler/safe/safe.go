package safe

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"oneinstack/core"
	"oneinstack/internal/models"
	safeservice "oneinstack/internal/services/safe"
	softwarehandler "oneinstack/router/handler/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
)

func GetFirewallInfo(c *gin.Context) {
	info, err := safeservice.NewDefaultService().Status(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"info": info})
}

func GetFirewallRules(c *gin.Context) {
	var param input.IptablesRuleParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	rules, err := safeservice.NewDefaultService().List(&param)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, rules)
}

func AddFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := safeservice.NewDefaultService().Add(c.Request.Context(), &param); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, param)
}

func UpdateFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := safeservice.NewDefaultService().Update(c.Request.Context(), &param); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, param)
}

func DeleteFirewallRule(c *gin.Context) {
	var param struct {
		ID int64 `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := safeservice.NewDefaultService().Delete(c.Request.Context(), param.ID); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

// StopFirewall 保留历史路由名称，实际按 enabled 字段设置目标状态，不再执行不确定的 toggle。
func StopFirewall(c *gin.Context) {
	var param input.FirewallToggleParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := safeservice.NewDefaultService().SetEnabled(
		c.Request.Context(), param.Enabled, param.Confirm,
	); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func BlockPing(c *gin.Context) {
	var param input.FirewallPingParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	if err := safeservice.NewDefaultService().SetPingBlocked(c.Request.Context(), param.Blocked); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func InstallFirewall(c *gin.Context) {
	status, err := safeservice.NewDefaultService().Status(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if status.Install && !(status.Backend == safeservice.BackendFirewalld && status.RepairRequired) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "服务器已安装受支持的防火墙"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	task, err := softwarehandler.SubmitInstallationTask(input.InstallParams{
		Key:     "firewalld",
		Version: "1.0.0",
	}, userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建 firewalld 安装任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{
		"taskId":      task.ID,
		"installName": task.ID,
		"operation":   task.Operation,
		"component":   task.Component,
		"status":      task.Status,
		"progress":    task.Progress,
		"statusUrl":   "/v1/soft/tasks/" + task.ID,
		"streamUrl":   "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

func handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, safeservice.ErrValidation):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙参数无效"))
	case errors.Is(err, safeservice.ErrProtected):
		core.HandleError(c, core.WrapError(err, core.ErrForbidden, "系统保护规则不可修改"))
	case errors.Is(err, safeservice.ErrUnsupported):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "当前防火墙不支持此操作"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "防火墙规则不存在"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "防火墙操作失败"))
	}
}
