package website

import (
	"strconv"

	"oneinstack/core"
	websiteService "oneinstack/internal/services/website"

	"github.com/gin-gonic/gin"
)

func GetWebsiteSettings(c *gin.Context) {
	id, ok := websiteIDParam(c)
	if !ok {
		return
	}
	service, err := websiteService.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	document, err := service.GetSettings(id)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取网站设置失败"))
		return
	}
	core.HandleSuccess(c, document)
}

func UpdateWebsiteSettings(c *gin.Context) {
	id, ok := websiteIDParam(c)
	if !ok {
		return
	}
	var request websiteService.WebsiteSettings
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "网站设置格式错误"))
		return
	}
	service, err := websiteService.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	document, err := service.UpdateSettings(c.Request.Context(), id, request)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "发布网站设置失败"))
		return
	}
	core.HandleSuccess(c, document)
}

func GetWebsiteLog(c *gin.Context) {
	id, ok := websiteIDParam(c)
	if !ok {
		return
	}
	lineLimit, _ := strconv.Atoi(c.DefaultQuery("lines", "200"))
	service, err := websiteService.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	document, err := service.ReadLog(id, c.DefaultQuery("type", "access"), lineLimit)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取网站日志失败"))
		return
	}
	core.HandleSuccess(c, document)
}

func GetWebsiteManagedConfig(c *gin.Context) {
	id, ok := websiteIDParam(c)
	if !ok {
		return
	}
	service, err := websiteService.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	document, err := service.ReadManagedConfig(c.Request.Context(), id)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "读取网站配置失败"))
		return
	}
	core.HandleSuccess(c, document)
}

func UpdateWebsiteManagedConfig(c *gin.Context) {
	id, ok := websiteIDParam(c)
	if !ok {
		return
	}
	var request websiteService.WebServerConfigUpdate
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "网站配置格式错误"))
		return
	}
	service, err := websiteService.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	result, err := service.UpdateManagedConfig(
		c.Request.Context(), id, request.Content, request.Revision,
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站配置校验或发布失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func websiteIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "网站 ID 无效"))
		return 0, false
	}
	return id, true
}
