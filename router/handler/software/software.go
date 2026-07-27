package software

import (
	"net/http"

	"oneinstack/core"
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
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
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

func GetSoftware(c *gin.Context) {
	var req input.SoftwareParam
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	data, err := softwareService.List(&req)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, data)
}

func GetLogContent(c *gin.Context) {
	param := c.Query("fn")
	if manager, err := getTaskManager(); err == nil {
		if task, taskErr := manager.Get(param); taskErr == nil && canAccessTask(c, task) {
			chunk, logErr := manager.ReadLog(task.ID, 0, 64*1024)
			if logErr != nil {
				core.HandleError(c, core.WrapError(logErr, core.ErrInternalError, "操作失败"))
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
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
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
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
		core.HandleError(c, appErr)
		return
	}
	ok := softwareService.Exploration(&req)
	core.HandleSuccess(c, ok)
}

func RemoveSoftware(c *gin.Context) {
	var req input.RemoveParams
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数错误")
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
	task, err := manager.SubmitUninstall(req.Name, req.Version, userID)
	if err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "创建卸载任务失败")
		core.HandleError(c, appErr)
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
