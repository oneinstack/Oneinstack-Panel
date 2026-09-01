package container

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	containerService "oneinstack/internal/services/container"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

const composeUploadLimit = 2 << 20

func ListCompose(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := service.ListComposeProjects(ctx)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}

func PreviewCompose(c *gin.Context) {
	request, err := bindComposePreviewRequest(c)
	if err != nil {
		badComposeRequest(c, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	content, err := resolveComposeContent(c, request.Content, request.TemplateID)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	preview, err := service.ComposePreview(ctx, action, request.Name, content, request.RemoveVolumes)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	core.HandleSuccess(c, preview)
}

func CreateCompose(c *gin.Context) {
	request, err := bindComposeRequest(c)
	if err != nil {
		badComposeRequest(c, err)
		return
	}
	if !request.Confirm || strings.TrimSpace(request.PreviewFingerprint) == "" {
		core.HandleError(c, core.NewError(core.ErrOperationNotConfirmed, "请先预览 Compose 创建操作并确认执行"))
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	content, err := resolveComposeContent(c, request.Content, request.TemplateID)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	preview, err := service.ComposePreview(ctx, "create", request.Name, content, false)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	if preview.PreviewFingerprint != strings.TrimSpace(request.PreviewFingerprint) {
		composeOperationError(c, containerService.ErrComposePreviewStale)
		return
	}
	path, contentHash, err := service.StageComposeContent(preview.Target, content)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{
		Operation: models.ContainerTaskOperationComposeCreate,
		Compose: &containerService.ComposeTaskRequest{
			ProjectName: preview.ProjectName, Target: preview.Target, ContentPath: path,
			ContentHash: contentHash, PreviewHash: preview.PreviewFingerprint, ManagedProject: true,
		},
	}, userID)
	if err != nil {
		containerService.RemoveComposeTaskContent(path)
		composeOperationError(c, err)
		return
	}
	recordAction(c, "container.compose.create", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func GetCompose(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.ComposeProjectDetail(ctx, c.Param("name"))
	if err != nil {
		composeOperationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func GetComposeConfig(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	view, err := service.GetComposeConfigView(ctx, c.Param("name"), false)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	core.HandleSuccess(c, view)
}

func RevealComposeConfig(c *gin.Context) {
	var request input.ContainerComposeConfigRevealRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请输入当前面板密码"))
		return
	}
	if !verifyCurrentPanelPassword(c, request.PanelPassword) {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	view, err := service.GetComposeConfigView(ctx, c.Param("name"), true)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	core.HandleSuccess(c, view)
}

func verifyCurrentPanelPassword(c *gin.Context, password string) bool {
	usernameValue, exists := c.Get(middleware.ContextUsername)
	username, ok := usernameValue.(string)
	if !exists || !ok || username == "" || strings.TrimSpace(password) == "" {
		return false
	}
	account, verified := userservice.CheckUserPassword(username, password)
	if !verified {
		return false
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	return ok && account.ID == userID
}

func UpdateComposeConfig(c *gin.Context) {
	request, err := bindComposeRequest(c)
	if err != nil {
		badComposeRequest(c, err)
		return
	}
	if request.TemplateID != 0 {
		badComposeRequest(c, errors.New("编辑 Compose 配置不支持直接使用模板"))
		return
	}
	request.Name = c.Param("name")
	if !request.Confirm || strings.TrimSpace(request.PreviewFingerprint) == "" {
		core.HandleError(c, core.NewError(core.ErrOperationNotConfirmed, "请先预览 Compose 编辑操作并确认执行"))
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	preview, err := service.ComposePreview(ctx, "edit", request.Name, request.Content, false)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	if preview.PreviewFingerprint != strings.TrimSpace(request.PreviewFingerprint) {
		composeOperationError(c, containerService.ErrComposePreviewStale)
		return
	}
	path, contentHash, err := service.StageComposeContent(preview.Target, preview.EffectiveContent)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{
		Operation: models.ContainerTaskOperationComposeEdit,
		Compose: &containerService.ComposeTaskRequest{
			ProjectName: preview.ProjectName, Target: preview.Target, ContentPath: path,
			ContentHash: contentHash, PreviewHash: preview.PreviewFingerprint, ManagedProject: preview.Target.Managed,
		},
	}, userID)
	if err != nil {
		containerService.RemoveComposeTaskContent(path)
		composeOperationError(c, err)
		return
	}
	recordAction(c, "container.compose.edit", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func ComposeAction(c *gin.Context) {
	request, err := bindComposeActionRequest(c)
	if err != nil {
		badComposeRequest(c, err)
		return
	}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action != "start" && action != "stop" && action != "restart" && action != "update" && action != "delete" {
		badComposeRequest(c, errors.New("不支持的 Compose 项目操作"))
		return
	}
	if action == "delete" && request.RemoveVolumes {
		access, _ := middleware.UserAccess(c)
		if access == nil || !access.HasPermission(accessservice.PermissionContainerVolumeWrite) {
			core.HandleError(c, core.NewError(core.ErrForbidden, "删除 Compose 项目卷需要存储卷管理权限"))
			return
		}
	}
	if (action == "update" || action == "delete") && (!request.Confirm || strings.TrimSpace(request.PreviewFingerprint) == "") {
		core.HandleError(c, core.NewError(core.ErrOperationNotConfirmed, "请先预览 Compose 操作并确认执行"))
		return
	}
	previewFingerprint := strings.TrimSpace(request.PreviewFingerprint)
	ctx, cancel := requestContext(c)
	defer cancel()
	target, err := service.ResolveComposeProject(ctx, c.Param("name"))
	if err != nil {
		composeOperationError(c, err)
		return
	}
	if action == "update" || action == "delete" {
		preview, previewErr := service.ComposePreview(ctx, action, c.Param("name"), "", request.RemoveVolumes)
		if previewErr != nil {
			composeOperationError(c, previewErr)
			return
		}
		if preview.PreviewFingerprint != previewFingerprint {
			composeOperationError(c, containerService.ErrComposePreviewStale)
			return
		}
	}
	operation := composeOperationForAction(action)
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{
		Operation: operation,
		Compose:   &containerService.ComposeTaskRequest{ProjectName: target.ProjectName, Target: target, PreviewHash: previewFingerprint, RemoveVolumes: request.RemoveVolumes, ManagedProject: target.Managed},
	}, userID)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	recordAction(c, "container.compose."+action, http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func ComposeLogs(c *gin.Context) {
	tail, err := strconv.Atoi(c.DefaultQuery("tail", "500"))
	if err != nil {
		containerLogBadRequest(c, errors.New("tail 必须是 1 到 10000 的整数"))
		return
	}
	timestamps, err := containerLogBool(c.Query("timestamps"), true, "timestamps")
	if err != nil {
		containerLogBadRequest(c, err)
		return
	}
	follow, err := containerLogBool(c.Query("follow"), false, "follow")
	if err != nil {
		containerLogBadRequest(c, err)
		return
	}
	options := containerService.ComposeLogOptions{Tail: tail, Since: strings.TrimSpace(c.Query("since")), Until: strings.TrimSpace(c.Query("until")), Timestamps: timestamps, Service: strings.TrimSpace(c.Query("service"))}
	ctx, cancel := requestContext(c)
	target, err := service.ResolveComposeProject(ctx, c.Param("name"))
	cancel()
	if err != nil {
		composeOperationError(c, err)
		return
	}
	if follow {
		followComposeLogs(c, target, options)
		return
	}
	ctx, cancel = requestContext(c)
	defer cancel()
	result, err := service.ComposeLogs(ctx, target, options)
	if err != nil {
		composeOperationError(c, err)
		return
	}
	recordAction(c, "container.compose.logs", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"projectName": target.ProjectName, "logs": result})
}

func followComposeLogs(c *gin.Context, target containerService.ComposeProjectTarget, options containerService.ComposeLogOptions) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.Writer.Flush()
	stream := &containerLogSSEWriter{writer: c.Writer}
	result := make(chan error, 1)
	go func() { result <- service.FollowComposeLogs(c.Request.Context(), target, options, stream) }()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case err := <-result:
			_ = stream.FlushPending()
			if err != nil && !errors.Is(err, context.Canceled) {
				recordAction(c, "container.compose.logs.follow", http.StatusInternalServerError, err)
				_ = stream.Event("error", gin.H{"message": "Compose 日志追踪已中断，请检查项目状态和 Docker 运行时"})
				return
			}
			_ = stream.Event("end", gin.H{"message": "Compose 日志追踪已结束"})
			return
		case <-heartbeat.C:
			if err := stream.Heartbeat(); err != nil {
				return
			}
		}
	}
}

func bindComposeRequest(c *gin.Context) (input.ContainerComposeRequest, error) {
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(composeUploadLimit + 64*1024); err != nil {
			return input.ContainerComposeRequest{}, err
		}
		request := input.ContainerComposeRequest{Name: c.PostForm("name"), Content: c.PostForm("content"), PreviewFingerprint: c.PostForm("previewFingerprint"), Confirm: strings.EqualFold(c.PostForm("confirm"), "true")}
		var err error
		request.TemplateID, err = parseFormUint(c.PostForm("templateId"))
		if err != nil {
			return input.ContainerComposeRequest{}, err
		}
		fileContent, err := readComposeUpload(c)
		if err != nil {
			return input.ContainerComposeRequest{}, err
		}
		if fileContent != "" {
			request.Content = fileContent
		}
		return request, nil
	}
	var request input.ContainerComposeRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return input.ContainerComposeRequest{}, err
	}
	return request, nil
}

func bindComposePreviewRequest(c *gin.Context) (input.ContainerComposePreviewRequest, error) {
	if strings.HasPrefix(strings.ToLower(c.GetHeader("Content-Type")), "multipart/form-data") {
		if err := c.Request.ParseMultipartForm(composeUploadLimit + 64*1024); err != nil {
			return input.ContainerComposePreviewRequest{}, err
		}
		request := input.ContainerComposePreviewRequest{Action: c.PostForm("action"), Name: c.PostForm("name"), Content: c.PostForm("content"), RemoveVolumes: strings.EqualFold(c.PostForm("removeVolumes"), "true")}
		var err error
		request.TemplateID, err = parseFormUint(c.PostForm("templateId"))
		if err != nil {
			return input.ContainerComposePreviewRequest{}, err
		}
		fileContent, err := readComposeUpload(c)
		if err != nil {
			return input.ContainerComposePreviewRequest{}, err
		}
		if fileContent != "" {
			request.Content = fileContent
		}
		return request, nil
	}
	var request input.ContainerComposePreviewRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return input.ContainerComposePreviewRequest{}, err
	}
	return request, nil
}

func bindComposeActionRequest(c *gin.Context) (input.ContainerComposeActionRequest, error) {
	var request input.ContainerComposeActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		return input.ContainerComposeActionRequest{}, err
	}
	return request, nil
}

func readComposeUpload(c *gin.Context) (string, error) {
	header, err := c.FormFile("file")
	if err != nil {
		return "", nil
	}
	extension := strings.ToLower(filepath.Ext(header.Filename))
	if extension != ".yml" && extension != ".yaml" {
		return "", errors.New("Compose 上传文件只支持 .yml 或 .yaml")
	}
	if header.Size > composeUploadLimit {
		return "", errors.New("Compose YAML 不能超过 2 MiB")
	}
	file, err := header.Open()
	if err != nil {
		return "", errors.New("无法读取 Compose 上传文件")
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, composeUploadLimit+1))
	if err != nil {
		return "", errors.New("无法读取 Compose 上传文件")
	}
	if len(content) > composeUploadLimit {
		return "", errors.New("Compose YAML 不能超过 2 MiB")
	}
	return string(content), nil
}

func resolveComposeContent(c *gin.Context, content string, templateID uint) (string, error) {
	content = strings.TrimSpace(content)
	if content != "" && templateID != 0 {
		return "", errors.New("Compose 内容和模板不能同时提交")
	}
	if templateID != 0 {
		ctx, cancel := requestContext(c)
		defer cancel()
		return service.ComposeTemplateContent(ctx, templateID)
	}
	return content, nil
}

func parseFormUint(value string) (uint, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == 0 {
		return 0, errors.New("模板 ID 无效")
	}
	return uint(parsed), nil
}

func composeOperationForAction(action string) string {
	return "compose." + strings.ToLower(strings.TrimSpace(action))
}

func badComposeRequest(c *gin.Context, err error) {
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "Compose 请求参数格式不正确"))
}

func composeOperationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, containerService.ErrComposeConfigInvalid):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrConfigValidateFailed, "Compose 配置无效", composeErrorMessage(err)))
	case errors.Is(err, containerService.ErrComposeProjectNotFound), errors.Is(err, containerService.ErrComposeTemplateNotFound):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrNotFound, "Compose 项目或模板不存在", composeErrorMessage(err)))
	case errors.Is(err, containerService.ErrComposeProjectConflict), errors.Is(err, containerService.ErrComposeProjectBusy), errors.Is(err, containerService.ErrComposePreviewStale), errors.Is(err, containerService.ErrComposeMultiFile):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrConflict, "Compose 操作冲突", composeErrorMessage(err)))
	case errors.Is(err, containerService.ErrComposeConfigUnavailable):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrConfigReadFailed, "Compose 配置不可用", composeErrorMessage(err)))
	case errors.Is(err, containerService.ErrComposeOperationFailed):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrOperationFailed, "Docker Compose 操作失败", composeErrorMessage(err)))
	case errors.Is(err, containerService.ErrComposeUnavailable):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrServiceUnavailable, "Docker Compose 插件不可用", "请安装与当前 Docker CLI 兼容的 Compose 插件后重试。"))
	case errors.Is(err, containerService.ErrRuntimeUnavailable):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrContainerRuntimeUnavailable, "Docker 运行时不可用", "请确认 Docker 服务已启动，并检查面板运行用户是否有访问 Docker socket 的权限。"))
	case errors.Is(err, containerService.ErrComposeOperationTimeout):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskTimeout, "Docker Compose 操作超时", "请检查 Docker daemon、网络、代理和镜像仓库状态后重试。"))
	case errors.Is(err, containerService.ErrInvalidLogOptions):
		containerLogBadRequest(c, err)
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "处理 Compose 请求失败"))
	}
}

func composeErrorMessage(err error) string {
	var composeErr *containerService.ComposeError
	if errors.As(err, &composeErr) && composeErr.Message != "" {
		return composeErr.Message
	}
	return "请检查 Compose 项目状态、配置文件权限和 Docker 运行时后重试。"
}
