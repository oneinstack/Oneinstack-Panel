package software

import (
	"errors"
	"net/http"
	"strings"

	"oneinstack/app"
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
		if handleInstallationParameterError(c, err) {
			return
		}
		appErr := core.WrapError(err, core.ErrBadRequest, "创建安装任务失败")
		core.HandleError(c, appErr)
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
		"taskId":        task.ID,
		"installName":   task.ID,
		"operation":     task.Operation,
		"component":     task.Component,
		"installSource": taskInstallSource(task),
		"summary":       "在线安装任务已创建",
		"status":        task.Status,
		"progress":      task.Progress,
		"statusUrl":     "/v1/soft/tasks/" + task.ID,
		"streamUrl":     "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

// RunOfflineInstallation accepts a Fail2ban component bundle and visible
// installation fields. The server derives the offline mode and bundle ID;
// neither is accepted as a client-provided parameter.
func RunOfflineInstallation(c *gin.Context) {
	maxPackageBytes := app.ONE_CONFIG.ScriptCenter.MaxPackageBytes
	if maxPackageBytes < 1 {
		maxPackageBytes = 64 << 20
	}
	if err := c.Request.ParseMultipartForm(maxPackageBytes); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "离线安装包请求格式不正确"))
		return
	}
	for _, key := range []string{"install-mode", "installMode", "offline-package-id", "offlinePackageID"} {
		if strings.TrimSpace(c.PostForm(key)) != "" {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, key+" 由 Panel 后端生成，不允许手工填写"))
			return
		}
	}
	header, err := c.FormFile("bundle")
	if err != nil {
		header, err = c.FormFile("file")
	}
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInvalidParameter, "请上传 Fail2ban 离线 Bundle"))
		return
	}
	if header.Size < 1 || header.Size > maxPackageBytes {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "离线 Bundle 大小超过限制"))
		return
	}
	bundle, err := header.Open()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "无法读取离线 Bundle"))
		return
	}
	defer bundle.Close()
	parameters := make(map[string]string)
	for _, key := range []string{
		"default-maxretry",
		"default-findtime",
		"default-bantime",
		"ignore-ip",
		"component-state-dir",
	} {
		if value := c.PostForm(key); value != "" {
			parameters[key] = value
		}
	}
	version := strings.TrimSpace(c.PostForm("version"))
	if version == "" {
		version = strings.TrimSpace(c.PostForm("software-version"))
	}
	key := strings.TrimSpace(c.PostForm("key"))
	if key == "" {
		key = "fail2ban"
	}
	req := input.InstallParams{
		Key:        key,
		Version:    version,
		Parameters: parameters,
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	task, err := SubmitOfflineInstallationTask(req, bundle, userID)
	if err != nil {
		if handleInstallationParameterError(c, err) {
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建离线安装任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
		"taskId":        task.ID,
		"installName":   task.ID,
		"operation":     task.Operation,
		"component":     task.Component,
		"installSource": taskInstallSource(task),
		"summary":       "离线安装任务已创建",
		"status":        task.Status,
		"progress":      task.Progress,
		"statusUrl":     "/v1/soft/tasks/" + task.ID,
		"streamUrl":     "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

func handleInstallationParameterError(c *gin.Context, err error) bool {
	var parameterErr *softwareService.InstallParameterError
	if !errors.As(err, &parameterErr) {
		return false
	}
	message := parameterErr.InstallationMessage()
	if message == "" {
		message = "安装参数无效，请检查字段类型、格式和取值范围后重试"
	}
	core.HandleSimpleError(c, core.NewError(core.ErrInvalidParameter, message))
	return true
}

func taskInstallSource(task *models.SoftwareTask) string {
	if task != nil && strings.EqualFold(strings.TrimSpace(task.InstallMode), "offline") {
		return "offline"
	}
	return "center"
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
	locale := middleware.RequestLocale(c)
	for index := range categories {
		categories[index].Name = i18n.LocalizeSoftwareCategory(locale, categories[index].Name)
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
	parameters := make(map[string]string, len(req.Parameters)+2)
	for key, value := range req.Parameters {
		parameters[key] = value
	}
	if req.DataPolicy != "" {
		parameters["data-policy"] = req.DataPolicy
	}
	if req.ConfirmDataDeletion {
		parameters["delete-data-confirm"] = "true"
	}
	task, err := manager.SubmitUninstallWithParameters(req.Name, req.Version, parameters, userID)
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
