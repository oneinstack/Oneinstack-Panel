package system

import (
	"errors"
	"oneinstack/core"
	auditservice "oneinstack/internal/services/audit"
	configsnapshot "oneinstack/internal/services/configsnapshot"
	"oneinstack/internal/services/system"
	configsnapshotHandler "oneinstack/router/handler/configsnapshot"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/router/session"

	"github.com/gin-gonic/gin"
)

type publicBaseInfoResponse struct {
	Title string `json:"title"`
}

func GetSystemInfo(c *gin.Context) {
	info, err := system.GetSystemInfo()
	if err != nil {
		appErr := core.WrapError(err, core.ErrSystemError, "获取系统信息失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, info)
}

func GetSystemMonitor(c *gin.Context) {
	monitor, err := system.GetSystemMonitor()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "获取系统监控信息失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, monitor)
}

func GetLibCount(c *gin.Context) {
	count, err := system.GetLibCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "获取软件数量失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

func GetWebSiteCount(c *gin.Context) {
	count, err := system.GetWebSiteCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "获取网站数量失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

// 获取备忘录数量
func GetRemarkCount(c *gin.Context) {
	count, err := system.GetRemarkCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "获取备忘录数量失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

func SystemInfo(c *gin.Context) {
	info, err := system.SystemInfo()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "获取系统信息失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, info)
}

func UpdateUser(c *gin.Context) {
	var request input.UpdateUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "用户资料格式不正确")
		core.HandleError(c, appErr)
		return
	}
	userID, exists := middleware.AuthenticatedUserID(c)
	if !exists {
		appErr := core.NewError(core.ErrUnauthorized, "登录状态无效")
		core.HandleError(c, appErr)
		return
	}
	if err := system.UpdateUser(userID, request.Username); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "更新用户资料失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func ResetPassword(c *gin.Context) {
	userID, exists := middleware.AuthenticatedUserID(c)
	if !exists {
		appErr := core.NewError(core.ErrUnauthorized, "登录状态无效")
		core.HandleError(c, appErr)
		return
	}
	var request input.ResetPasswordRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "密码修改参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	if err := system.ResetPassword(userID, request.CurrentPassword, request.Password); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "密码修改失败")
		var strengthErr *system.PasswordStrengthError
		if errors.As(err, &strengthErr) {
			appErr = core.NewFieldError(core.ErrBadRequest, strengthErr.Error(), "password")
		} else if errors.Is(err, system.ErrCurrentPasswordInvalid) {
			auditservice.RecordAuthEvent(c, "auth.password_change", "", userID, 401, "failure", "", "当前密码错误")
			appErr = core.NewError(core.ErrInvalidPassword, "当前密码错误")
		}
		core.HandleError(c, appErr)
		return
	}
	auditservice.RecordAuthEvent(c, "auth.password_change", "", userID, 200, "success", "", "")
	session.Clear(c)
	core.HandleSuccess(c, gin.H{
		"authenticated":            false,
		"reauthenticationRequired": true,
	})
}

func UpdatePort(c *gin.Context) {
	param := input.UpdatePortRequest{}
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "面板访问配置参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := system.UpdateSystemPort(param.Port)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "更新面板端口失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func GetPanelNetwork(c *gin.Context) {
	settings, err := system.GetPanelNetworkSettings()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "获取面板访问配置失败"))
		return
	}
	core.HandleSuccess(c, settings)
}

func GetPanelEntryStatus(c *gin.Context) {
	core.HandleSuccess(c, system.GetPanelEntryStatus())
}

func GetPanelNetworkTransaction(c *gin.Context) {
	transaction, err := system.GetPanelNetworkTransaction(c.Param("id"))
	if err != nil {
		if errors.Is(err, system.ErrNetworkTransactionNotFound) {
			core.HandleError(c, core.WrapError(err, core.ErrNotFound, "访问配置应用任务不存在"))
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "获取访问配置应用状态失败"))
		return
	}
	core.HandleSuccess(c, transaction)
}

func UpdatePanelNetwork(c *gin.Context) {
	var request input.UpdatePanelNetworkRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "面板访问配置参数格式不正确"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
		return
	}
	before, err := system.GetPanelNetworkSettings()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取面板当前访问配置失败"))
		return
	}
	snapshot, err := configsnapshot.Default().Create(configsnapshot.CreateInput{
		ResourceType: "panel_access", ResourceID: "panel", Operation: "update",
		Before: before, After: request, RequestedBy: userID,
	})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "创建面板访问配置快照失败"))
		return
	}
	settings, err := system.UpdatePanelNetwork(system.UpdatePanelNetworkRequest{
		BindAddress:          request.BindAddress,
		HTTPPort:             request.HTTPPort,
		HTTPSEnabled:         request.HTTPSEnabled,
		HTTPSPort:            request.HTTPSPort,
		HTTPSCertificateFile: request.HTTPSCertificateFile,
		HTTPSPrivateKeyFile:  request.HTTPSPrivateKeyFile,
		TrustedProxies:       request.TrustedProxies,
		PanelEntryEnabled:    request.PanelEntryEnabled,
		PanelEntryPath:       request.PanelEntryPath,
		RotatePanelEntry:     request.RotatePanelEntry,
	})
	if err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		if errors.Is(err, system.ErrNetworkConfigInvalid) {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "面板访问配置无效"))
			return
		}
		if errors.Is(err, system.ErrNetworkApplyInProgress) {
			core.HandleError(c, core.WrapError(err, core.ErrConflict, "已有访问配置正在应用，请稍后重试"))
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "保存面板访问配置失败"))
		return
	}
	if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, settings, "succeeded", ""); err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", "保存面板访问配置快照最终状态失败")
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "更新面板访问配置快照状态失败"))
		return
	}
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "面板访问配置已更新")
	core.HandleSuccess(c, settings)
}

func UpdateSystemTitle(c *gin.Context) {
	param := input.UpdateSystemTitleRequest{}
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "系统标题参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := system.UpdateSystemTitle(param.Title)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "更新系统标题失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func GetInfo(c *gin.Context) {
	info, err := system.GetInfo()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "读取面板基础信息失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, publicBaseInfoResponse{Title: info.Title})
}
