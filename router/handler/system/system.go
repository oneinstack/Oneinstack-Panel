package system

import (
	"errors"
	"oneinstack/core"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/internal/services/system"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/router/session"

	"github.com/gin-gonic/gin"
)

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
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, monitor)
}

func GetLibCount(c *gin.Context) {
	count, err := system.GetLibCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

func GetWebSiteCount(c *gin.Context) {
	count, err := system.GetWebSiteCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

// 获取备忘录数量
func GetRemarkCount(c *gin.Context) {
	count, err := system.GetRemarkCount()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, count)
}

func SystemInfo(c *gin.Context) {
	info, err := system.SystemInfo()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, info)
}

func UpdateUser(c *gin.Context) {
	var request input.UpdateUserRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
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
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
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
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	if err := system.ResetPassword(userID, request.CurrentPassword, request.Password); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "密码修改失败")
		if errors.Is(err, system.ErrCurrentPasswordInvalid) {
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
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := system.UpdateSystemPort(param.Port)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
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

func UpdatePanelNetwork(c *gin.Context) {
	var request input.UpdatePanelNetworkRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		if errors.Is(err, system.ErrNetworkConfigInvalid) {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "面板访问配置无效"))
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "保存面板访问配置失败"))
		return
	}
	core.HandleSuccess(c, settings)
}

func UpdateSystemTitle(c *gin.Context) {
	param := input.UpdateSystemTitleRequest{}
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	err := system.UpdateSystemTitle(param.Title)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func GetInfo(c *gin.Context) {
	info, err := system.GetInfo()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, info)
}
