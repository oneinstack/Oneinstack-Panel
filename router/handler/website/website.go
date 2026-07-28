package website

import (
	"errors"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func List(c *gin.Context) {
	input := &input.WebsiteQueryParam{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数格式错误")
		core.HandleError(c, appErr)
		return
	}
	list, err := website.List(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "操作失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, list)
}

func Add(c *gin.Context) {
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数格式错误")
		core.HandleError(c, appErr)
		return
	}
	err := website.Add(input)
	if err != nil {
		message := "网站配置发布失败"
		if errors.Is(err, website.ErrNginxUnavailable) {
			message = "未检测到可用 Nginx，请先安装并确保服务可执行文件可被面板访问"
		}
		appErr := core.WrapError(err, core.ErrConfigError, message)
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func Update(c *gin.Context) {
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "请求参数格式错误")
		core.HandleError(c, appErr)
		return
	}
	err := website.Update(input)
	if err != nil {
		message := "网站配置更新失败"
		if errors.Is(err, website.ErrNginxUnavailable) {
			message = "未检测到可用 Nginx，请先安装并确保服务可执行文件可被面板访问"
		}
		appErr := core.WrapError(err, core.ErrConfigError, message)
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "更新成功")
}

func Delete(c *gin.Context) {
	var request input.WebsiteDeleteParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数格式错误"))
		return
	}
	if request.ID <= 0 || request.ConfirmName == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入网站名称确认删除"))
		return
	}
	manager, err := DefaultWebsiteTaskManager()
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "网站任务服务不可用")
		core.HandleError(c, appErr)
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitDelete(
		request.ID,
		request.DatabaseID,
		request.DeleteFiles,
		request.ConfirmName,
		userID,
	)
	if err != nil {
		handleWebsiteTaskError(c, err, "创建网站安全删除任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(task))
}

func Info(context *gin.Context) {
	check := website.Check()
	core.HandleSuccess(context, check)
}
