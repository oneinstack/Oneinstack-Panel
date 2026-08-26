package website

import (
	"errors"
	"net/http"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"strconv"

	"github.com/gin-gonic/gin"
)

func List(c *gin.Context) {
	input := &input.WebsiteQueryParam{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "网站列表查询参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	list, err := website.List(input)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "查询网站列表失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, list)
}

func handleWebsiteOwnershipError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, website.ErrWebsiteWebServerMismatch):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"网站归属 Web Server 不一致",
			err.Error(),
		))
		return true
	case errors.Is(err, website.ErrWebsiteEngineImmutable):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInvalidParameter,
			"网站归属 Web Server 不可修改",
			err.Error(),
		))
		return true
	case errors.Is(err, website.ErrWebsiteConfigUnavailable):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"网站运行配置不可用",
			err.Error(),
		))
		return true
	default:
		return false
	}
}

func SetStatus(c *gin.Context) {
	if !rejectDirectMutation(c, "website.toggle") {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "id 必须是正整数", "id"))
		return
	}
	var request input.WebsiteStatusParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "网站状态更新参数格式不正确"))
		return
	}
	service, err := website.DefaultService()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站服务不可用"))
		return
	}
	site, err := service.SetEnabled(c.Request.Context(), id, request.Enabled)
	if err != nil {
		if handleWebsiteOwnershipError(c, err) {
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "网站状态切换失败"))
		return
	}
	core.HandleSuccess(c, site)
}

func Add(c *gin.Context) {
	if !rejectDirectMutation(c, "website.create") {
		return
	}
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "网站创建参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := website.Add(input)
	if err != nil {
		if handleWebsiteOwnershipError(c, err) {
			return
		}
		if errors.Is(err, website.ErrWebsiteRootInvalid) {
			core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "网站根目录必须是受管网站根目录下的相对目录，不能越界或包含符号链接", "root_dir"))
			return
		}
		message := "网站配置发布失败"
		if errors.Is(err, website.ErrNginxUnavailable) {
			message = "未检测到可用 Web 引擎，请先安装并确保服务可执行文件可被面板访问"
		}
		appErr := core.WrapError(err, core.ErrConfigError, message)
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, "创建成功")

}

func Update(c *gin.Context) {
	if !rejectDirectMutation(c, "website.update") {
		return
	}
	input := &models.Website{}
	if err := c.ShouldBindJSON(&input); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "网站更新参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	err := website.Update(input)
	if err != nil {
		if handleWebsiteOwnershipError(c, err) {
			return
		}
		message := "网站配置更新失败"
		if errors.Is(err, website.ErrNginxUnavailable) {
			message = "未检测到可用 Web 引擎，请先安装并确保服务可执行文件可被面板访问"
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "网站删除参数格式不正确"))
		return
	}
	if request.ID <= 0 || request.ConfirmName == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入网站名称确认删除"))
		return
	}
	if shouldRequestWebsiteApproval(c) {
		approval, reused, err := createOrReuseWebsiteApproval(c, ApprovalActionWebsiteDelete, request.ConfirmName, strconv.FormatInt(request.ID, 10), DeleteApprovalPayload{
			ID:          request.ID,
			DatabaseID:  request.DatabaseID,
			DeleteFiles: request.DeleteFiles,
			ConfirmName: request.ConfirmName,
		})
		if err != nil {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建网站删除审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
			"reused":     reused,
		}))
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
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func Info(context *gin.Context) {
	check := website.Check()
	core.HandleSuccess(context, check)
}
