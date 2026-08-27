package software

import (
	"net/http"

	"oneinstack/core"
	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
	softwareService "oneinstack/internal/services/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
)

func RunInstallation(c *gin.Context) {
	var req input.InstallParams
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "软件安装参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	task, err := SubmitInstallationTask(req, userID)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "创建安装任务失败")
		core.HandleError(c, appErr)
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
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

func GetSoftware(c *gin.Context) {
	var req input.SoftwareParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "软件列表参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	data, err := softwareService.List(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "查询软件列表失败")
		core.HandleError(c, appErr)
		return
	}
	for index := range data.Data {
		data.Data[index].Describe = i18n.LocalizeSoftwareDescription(
			middleware.RequestLocale(c),
			data.Data[index].Key,
			data.Data[index].Describe,
		)
	}
	core.HandleSuccess(c, data)
}

func ListSoftwareCategories(c *gin.Context) {
	var req input.SoftwareCategoryParam
	if err := c.ShouldBindQuery(&req); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "软件分类查询参数格式不正确"))
		return
	}
	categories, err := softwareService.ListCategories(&req)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "查询软件分类失败"))
		return
	}
	core.HandleSuccess(c, categories)
}

func GetLogContent(c *gin.Context) {
	param := c.Query("fn")
	if manager, err := getTaskManager(); err == nil {
		if task, taskErr := manager.Get(param); taskErr == nil && canAccessTask(c, task) {
			chunk, logErr := manager.ReadLog(task.ID, 0, 64*1024)
			if logErr != nil {
				core.HandleError(c, core.WrapError(logErr, core.ErrInternalError, "读取软件任务日志失败"))
				return
			}
			core.HandleSuccess(c, gin.H{
				"logs":      chunk.Content,
				"completed": models.IsSoftwareTaskTerminal(task.Status),
				"taskId":    task.ID,
			})
			return
		}
	}
	softName := c.Query("name")
	install, err := utils.GetLogContent(param, softName)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "读取软件安装日志失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, gin.H{
		"logs": install,
	})
}

func Exploration(c *gin.Context) {
	var req input.SoftwareParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "软件探测参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	ok := softwareService.Exploration(&req)
	core.HandleSuccess(c, ok)
}

func RemoveSoftware(c *gin.Context) {
	var req input.RemoveParams
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "软件卸载参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	manager, err := getTaskManager()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "卸载任务服务不可用")
		core.HandleError(c, appErr)
		return
	}
	task, err := manager.SubmitUninstallWithParameters(req.Name, req.Version, req.Parameters, userID)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "创建卸载任务失败")
		core.HandleError(c, appErr)
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
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
