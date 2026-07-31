package website

import (
	"errors"
	"net/http"
	"strings"

	"oneinstack/core"
	websiteService "oneinstack/internal/services/website"

	"github.com/gin-gonic/gin"
)

func GetWebServerStatus(c *gin.Context) {
	core.HandleSuccess(c, websiteService.WebServerStatus())
}

func ListWebServerConfigs(c *gin.Context) {
	manager, err := websiteService.NewDefaultWebServerConfigManager()
	if err != nil {
		handleWebServerConfigError(c, err, "读取 Web 服务器配置失败")
		return
	}
	files, err := manager.List()
	if err != nil {
		handleWebServerConfigError(c, err, "读取 Web 服务器配置失败")
		return
	}
	core.HandleSuccess(c, gin.H{
		"server": manager.Server,
		"files":  files,
	})
}

func GetWebServerConfig(c *gin.Context) {
	path := strings.TrimSpace(c.Query("path"))
	if path == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请选择配置文件"))
		return
	}
	manager, err := websiteService.NewDefaultWebServerConfigManager()
	if err != nil {
		handleWebServerConfigError(c, err, "读取 Web 服务器配置失败")
		return
	}
	document, err := manager.Read(path)
	if err != nil {
		handleWebServerConfigError(c, err, "读取 Web 服务器配置失败")
		return
	}
	core.HandleSuccess(c, document)
}

func UpdateWebServerConfig(c *gin.Context) {
	var request websiteService.WebServerConfigUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "配置内容格式错误"))
		return
	}
	manager, err := websiteService.NewDefaultWebServerConfigManager()
	if err != nil {
		handleWebServerConfigError(c, err, "保存 Web 服务器配置失败")
		return
	}
	result, err := manager.Update(c.Request.Context(), request)
	if err != nil {
		handleWebServerConfigError(c, err, "保存 Web 服务器配置失败")
		return
	}
	c.JSON(http.StatusOK, core.SuccessResponse(result))
}

func handleWebServerConfigError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, websiteService.ErrWebServerUnavailable):
		core.HandleError(c, core.WrapError(
			err,
			core.ErrConfigError,
			"未检测到可管理的 Nginx 或 OpenResty",
		))
	case errors.Is(err, websiteService.ErrWebServerConfigConflict):
		core.HandleError(c, core.WrapError(
			err,
			core.ErrConflict,
			"配置文件已发生变化，请重新读取后再保存",
		))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, message))
	}
}
