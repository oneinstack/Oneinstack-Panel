package operationpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	auditservice "oneinstack/internal/services/audit"
	configsnapshot "oneinstack/internal/services/configsnapshot"
	fail2banservice "oneinstack/internal/services/fail2ban"
	previewservice "oneinstack/internal/services/operationpreview"
	safeservice "oneinstack/internal/services/safe"
	softwareService "oneinstack/internal/services/software"
	systemservice "oneinstack/internal/services/system"
	"oneinstack/internal/services/website"
	"oneinstack/router/handler/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type previewRequest struct {
	Operation string          `json:"operation" binding:"required"`
	Payload   json.RawMessage `json:"payload"`
}

type executeRequest struct {
	Confirm bool `json:"confirm"`
}

type websiteSettingsUpdatePayload struct {
	ID        int64                   `json:"id"`
	WebsiteID int64                   `json:"websiteId"`
	Settings  website.WebsiteSettings `json:"settings"`
}

type websiteConfigUpdatePayload struct {
	ID        int64  `json:"id"`
	WebsiteID int64  `json:"websiteId"`
	Content   string `json:"content"`
	Revision  string `json:"revision"`
}

var errUnsupportedWebsiteOperation = errors.New("unsupported website operation")

func websitePayloadID(id, websiteID int64) int64 {
	if id > 0 {
		return id
	}
	return websiteID
}

func websiteTargetVersion(id int64, revision string) string {
	return fmt.Sprintf("website|id=%d|revision=%s", id, revision)
}

type webServerPreviewPresentation struct {
	Name            string
	SyntaxName      string
	ValidateName    string
	ValidateCommand string
	ReloadName      string
	ReloadCommand   string
	Service         string
}

func webServerPreviewForEngine(engine string) webServerPreviewPresentation {
	component := strings.ToLower(strings.TrimSpace(engine))
	if component == "" {
		server := website.WebServerStatus()
		return webServerPreviewForServer(server)
	}
	return webServerPreviewForServer(website.WebServerInfo{Component: component})
}

func webServerPreviewForServer(server website.WebServerInfo) webServerPreviewPresentation {
	component := strings.ToLower(strings.TrimSpace(server.Component))
	presentation := webServerPreviewPresentation{
		Name:            strings.TrimSpace(server.Name),
		ValidateCommand: fmt.Sprintf("%s -t", webServerDisplayBinary(server.BinaryPath, component)),
		ReloadCommand:   fmt.Sprintf("%s -s reload", webServerDisplayBinary(server.BinaryPath, component)),
		Service:         strings.TrimSpace(server.ServiceName),
	}
	switch component {
	case "nginx":
		presentation.Name = "Nginx"
		presentation.Service = defaultWebServerService(component, presentation.Service)
	case "openresty":
		presentation.Name = "OpenResty"
		presentation.Service = defaultWebServerService(component, presentation.Service)
	case "tengine":
		presentation.Name = "Tengine"
		presentation.Service = defaultWebServerService(component, presentation.Service)
	case "apache":
		presentation.Name = "Apache"
		presentation.ValidateCommand = fmt.Sprintf("%s -t -f <config>", webServerDisplayBinary(server.BinaryPath, component))
		presentation.ReloadCommand = fmt.Sprintf("systemctl reload %s.service", defaultWebServerService(component, presentation.Service))
		presentation.Service = defaultWebServerService(component, presentation.Service)
	case "caddy":
		presentation.Name = "Caddy"
		presentation.ValidateCommand = fmt.Sprintf("%s validate --config <config> --adapter caddyfile", webServerDisplayBinary(server.BinaryPath, component))
		presentation.ReloadCommand = fmt.Sprintf("%s reload --config <config> --adapter caddyfile --force", webServerDisplayBinary(server.BinaryPath, component))
		presentation.Service = defaultWebServerService(component, presentation.Service)
	default:
		if presentation.Name == "" {
			presentation.Name = "Web Server"
		}
		presentation.Service = defaultWebServerService(component, presentation.Service)
	}
	presentation.SyntaxName = presentation.Name + " 配置语法"
	presentation.ValidateName = "校验 " + presentation.Name + " 配置"
	presentation.ReloadName = "重新加载 " + presentation.Name
	return presentation
}

func webServerDisplayBinary(binary, component string) string {
	if value := strings.TrimSpace(binary); value != "" {
		base := filepath.Base(value)
		if base != "." && base != string(filepath.Separator) && base != "" {
			return base
		}
	}
	switch component {
	case "apache":
		return "httpd"
	case "caddy":
		return "caddy"
	default:
		return "nginx"
	}
}

func defaultWebServerService(component, service string) string {
	if strings.TrimSpace(service) != "" {
		return strings.TrimSpace(service)
	}
	switch component {
	case "nginx":
		return "oneinstack-nginx"
	case "openresty":
		return "oneinstack-openresty"
	case "tengine":
		return "oneinstack-tengine"
	case "apache":
		return "oneinstack-httpd"
	case "caddy":
		return "oneinstack-caddy"
	default:
		return ""
	}
}

func webServerPreviewActions(server website.WebServerInfo) []previewservice.Action {
	presentation := webServerPreviewForServer(server)
	return webServerPreviewActionsForPresentation(presentation)
}

func webServerPreviewActionsForEngine(engine string) []previewservice.Action {
	return webServerPreviewActionsForPresentation(webServerPreviewForEngine(engine))
}

func webServerPreviewActionsForPresentation(presentation webServerPreviewPresentation) []previewservice.Action {
	return []previewservice.Action{
		{Type: "command", Name: presentation.ValidateName, DisplayCommand: presentation.ValidateCommand},
		{Type: "service", Name: presentation.ReloadName, DisplayCommand: presentation.ReloadCommand, Service: presentation.Service},
	}
}

func websiteRuntimeDocument(operation string, runtime website.WebsiteRuntimePreview) (previewservice.Document, string) {
	presentation := webServerPreviewForEngine(runtime.Website.Engine)
	document := previewservice.Document{
		Review:    previewservice.Review{Required: true, RiskLevel: "high", Reason: fmt.Sprintf("网站 %s（%s）将修改运行配置或流量路径，执行前需要确认", runtime.Website.Name, runtime.Website.Domain)},
		Files:     []previewservice.FileChange{{Path: runtime.AfterPath, Action: "update", ChangeSummary: "更新网站受管虚拟主机配置", Diff: boundedConfigDiff(runtime.BeforeContent, runtime.AfterContent)}},
		Prechecks: []previewservice.Precheck{{Name: "网站配置版本", Status: "passed", Message: "预览基于当前网站和运行配置生成"}, {Name: presentation.SyntaxName, Status: "deferred", Message: "执行阶段将重新校验"}},
		Actions:   webServerPreviewActionsForEngine(runtime.Website.Engine),
		Impact:    previewservice.Impact{WriteFiles: runtime.BeforePath != runtime.AfterPath || runtime.BeforeContent != runtime.AfterContent, ModifyDatabase: operation != "website.config.update", ReloadService: runtime.Reload},
		Rollback:  previewservice.Rollback{Supported: true, Summary: "执行前创建配置快照；发布或重载失败时恢复原配置"},
	}
	if !runtime.Reload {
		document.Actions = document.Actions[:1]
	}
	if operation == "website.toggle" {
		document.Files[0].Action = "enable_or_remove"
		document.Files[0].ChangeSummary = "启用或停用网站虚拟主机配置；网站数据和网站文件保留"
		document.Impact.NetworkRisk = true
	}
	return document, websiteTargetVersion(runtime.Website.ID, runtime.CurrentVersion)
}

func boundedConfigDiff(before, after string) string {
	if before == after {
		return ""
	}
	const maxDiffBytes = 128 << 10
	var builder strings.Builder
	beforeLines := strings.Split(strings.ReplaceAll(before, "\r\n", "\n"), "\n")
	afterLines := strings.Split(strings.ReplaceAll(after, "\r\n", "\n"), "\n")
	builder.WriteString("--- current\n+++ proposed\n")
	limit := len(beforeLines)
	if len(afterLines) > limit {
		limit = len(afterLines)
	}
	for index := 0; index < limit; index++ {
		var oldLine, newLine string
		if index < len(beforeLines) {
			oldLine = beforeLines[index]
		}
		if index < len(afterLines) {
			newLine = afterLines[index]
		}
		if oldLine == newLine {
			continue
		}
		if index < len(beforeLines) {
			builder.WriteString("-" + oldLine + "\n")
		}
		if index < len(afterLines) {
			builder.WriteString("+" + newLine + "\n")
		}
		if builder.Len() >= maxDiffBytes {
			return builder.String()[:maxDiffBytes] + "\n... diff truncated"
		}
	}
	return builder.String()
}

var fail2banPreviewLimiter = middleware.NewRateLimiter(10, time.Minute)

func Preview(c *gin.Context) {
	var request previewRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "预览请求格式错误"))
		return
	}
	operation := strings.TrimSpace(request.Operation)
	if !supportedOperation(operation) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "不支持的操作类型"))
		return
	}
	if err := requireOperationPermission(c, operation); err != nil {
		core.HandleError(c, err)
		return
	}
	if err := requireServiceComponentPermission(c, operation, request.Payload); err != nil {
		core.HandleError(c, err)
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	if operation == "fail2ban.ban" || operation == "fail2ban.unban" {
		target, ok := fail2banPreviewTarget(request.Payload)
		if ok {
			key := fmt.Sprintf("%d|%s|%s", userID, operation, target)
			allowed, retryAfter := fail2banPreviewLimiter.Allow(key)
			if !allowed {
				seconds := max(1, int(math.Ceil(retryAfter.Seconds())))
				c.Header("Retry-After", fmt.Sprint(seconds))
				action := "封禁"
				if operation == "fail2ban.unban" {
					action = "解封"
				}
				core.HandleErrorWithStatus(c, http.StatusTooManyRequests, core.NewErrorWithDetail(
					core.ErrRateLimitExceeded,
					fmt.Sprintf("%s预览请求过于频繁：%s", action, target),
					fmt.Sprintf("请在 %d 秒后重试。", seconds),
				))
				return
			}
		}
	}
	payload := request.Payload
	if operation == "website.create" {
		err := error(nil)
		payload, err = normalizeWebsiteCreatePayload(payload)
		if err != nil {
			if handleWebsitePreviewError(c, operation, err) {
				return
			}
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "网站创建参数无效"))
			return
		}
	}
	if operation == "fail2ban.policy_change" {
		var err error
		payload, err = normalizeFail2banPolicyChangePayload(payload, userID)
		if err != nil {
			if handleFail2banPreviewError(c, err) {
				return
			}
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "入侵防护预览参数无效"))
			return
		}
	}
	document, resourceVersion, err := buildDocument(operation, payload)
	if err != nil {
		if handleWebsitePreviewError(c, operation, err) {
			return
		}
		if errors.Is(err, safeservice.ErrValidation) {
			message := safeservice.ValidationMessage(err)
			if message == "" {
				message = "防火墙参数无效"
			}
			core.HandleError(c, core.NewError(core.ErrBadRequest, message))
		} else {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "生成操作预览失败"))
		}
		return
	}
	service := previewservice.New(app.DB())
	created, err := service.Create(operation, userID, payload, document, resourceVersion)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "保存操作预览失败"))
		return
	}
	localizeDocument(c.GetString("locale"), created)
	core.HandleSuccess(c, created)
}

func isWebsiteOperation(operation string) bool {
	switch operation {
	case "website.create", "website.update", "website.settings.update",
		"website.config.update", "website.webserver.config.update", "website.toggle":
		return true
	default:
		return false
	}
}

func isJSONDecodeError(err error) bool {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr)
}

func handleWebsitePreviewError(c *gin.Context, operation string, err error) bool {
	if !isWebsiteOperation(operation) {
		return false
	}
	switch {
	case isJSONDecodeError(err):
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "网站操作参数格式错误"))
	case errors.Is(err, website.ErrWebsiteRootInvalid):
		core.HandleError(c, core.NewFieldError(
			core.ErrInvalidParameter,
			"网站根目录必须是受管网站根目录下的相对目录，不能越界或包含符号链接",
			"root_dir",
		))
	case errors.Is(err, website.ErrWebsiteIDRequired):
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "网站操作缺少 websiteId 或 id"))
	case errors.Is(err, website.ErrWebsiteWebServerMismatch):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"网站归属 Web Server 不一致",
			err.Error(),
		))
	case errors.Is(err, website.ErrWebsiteEngineImmutable):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInvalidParameter,
			"网站归属 Web Server 不可修改",
			err.Error(),
		))
	case errors.Is(err, website.ErrWebsiteConfigUnavailable):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"网站运行配置不可用",
			err.Error(),
		))
	case errors.Is(err, website.ErrWebsiteSettingsValidate):
		detail := strings.TrimSpace(strings.TrimPrefix(
			err.Error(), website.ErrWebsiteSettingsValidate.Error()+":",
		))
		if detail == "" {
			detail = "请检查网站设置字段后重试。"
		}
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInvalidParameter,
			"网站设置格式错误",
			detail,
		))
	case errors.Is(err, website.ErrWebsiteParameterInvalid):
		detail := strings.TrimSpace(strings.TrimPrefix(
			err.Error(), website.ErrWebsiteParameterInvalid.Error()+":",
		))
		if detail == "" {
			detail = "请检查网站名称、域名、类型和相关参数后重试。"
		}
		core.HandleError(c, core.NewErrorWithDetail(core.ErrInvalidParameter, "网站参数无效", detail))
	case errors.Is(err, website.ErrWebServerConfigValidate):
		core.HandleError(c, core.WrapError(
			err,
			core.ErrConfigValidateFailed,
			"Web Server 配置语法校验失败，请根据诊断信息修正后重新预览",
		))
	case errors.Is(err, website.ErrWebServerConfigConflict):
		core.HandleError(c, core.NewError(core.ErrConflict, "配置已发生变化，请重新预览后再执行"))
	case errors.Is(err, website.ErrWebsiteConflict):
		core.HandleError(c, core.NewError(core.ErrConflict, "网站已存在，请检查网站名称或域名"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.NewError(core.ErrWebsiteNotFound, "网站不存在或已被删除，请刷新后重试"))
	case errors.Is(err, website.ErrWebServerUnavailable):
		core.HandleError(c, core.NewError(core.ErrConfigError, "未检测到可管理的 Nginx 或 OpenResty"))
	case errors.Is(err, errUnsupportedWebsiteOperation):
		core.HandleError(c, core.NewError(core.ErrBadRequest, "不支持的网站更新操作"))
	default:
		return false
	}
	return true
}

func fail2banPreviewTarget(payload json.RawMessage) (string, bool) {
	var request fail2banservice.BanRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return "", false
	}
	if ip := net.ParseIP(strings.TrimSpace(request.IP)); ip != nil {
		return ip.String(), true
	}
	incidentID := strings.TrimSpace(request.IncidentID)
	if incidentID != "" && len(incidentID) <= 64 {
		return "incident:" + incidentID, true
	}
	return "", false
}

func normalizeFail2banPolicyChangePayload(payload json.RawMessage, userID int64) (json.RawMessage, error) {
	var request fail2banservice.PolicyChangeRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	normalized, _, err := fail2banservice.DefaultService().NormalizePolicyChange(request, userID)
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("normalize fail2ban policy payload: %w", err)
	}
	return result, nil
}

func handleFail2banPreviewError(c *gin.Context, err error) bool {
	switch {
	case isJSONDecodeError(err):
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "入侵防护参数格式错误"))
	case errors.Is(err, fail2banservice.ErrValidation):
		detail := strings.TrimSpace(strings.TrimPrefix(err.Error(), fail2banservice.ErrValidation.Error()+":"))
		if detail == "" {
			detail = "请检查策略动作、模板和参数取值后重试。"
		}
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrValidationFailed,
			"入侵防护参数无效，请检查后重试",
			detail,
		))
	case errors.Is(err, fail2banservice.ErrRevisionConflict):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(
			core.ErrResourceStateInvalid,
			"规则已被其他操作修改，请刷新后重试",
		))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.NewError(core.ErrNotFound, "目标入侵防护策略不存在，请刷新后重试"))
	case errors.Is(err, fail2banservice.ErrUnavailable):
		core.HandleError(c, core.NewError(core.ErrServiceUnavailable, "Fail2ban 未安装、未验证或服务不可用"))
	default:
		return false
	}
	return true
}

func localizeDocument(locale string, document *previewservice.Document) {
	if document == nil {
		return
	}
	document.Review.Reason = i18n.LocalizeBusinessText(locale, document.Review.Reason)
	for index := range document.Files {
		document.Files[index].Path = i18n.LocalizeBusinessText(locale, document.Files[index].Path)
		document.Files[index].ChangeSummary = i18n.LocalizeBusinessText(locale, document.Files[index].ChangeSummary)
	}
	for index := range document.Actions {
		document.Actions[index].Name = i18n.LocalizeBusinessText(locale, document.Actions[index].Name)
		document.Actions[index].DisplayCommand = i18n.LocalizeBusinessText(locale, document.Actions[index].DisplayCommand)
		document.Actions[index].Service = i18n.LocalizeBusinessText(locale, document.Actions[index].Service)
	}
	for index := range document.Prechecks {
		document.Prechecks[index].Name = i18n.LocalizeBusinessText(locale, document.Prechecks[index].Name)
		document.Prechecks[index].Message = i18n.LocalizeBusinessText(locale, document.Prechecks[index].Message)
	}
	document.Rollback.Summary = i18n.LocalizeBusinessText(locale, document.Rollback.Summary)
	for index := range document.Rollback.Unrecoverable {
		document.Rollback.Unrecoverable[index] = i18n.LocalizeBusinessText(locale, document.Rollback.Unrecoverable[index])
	}
}

func Execute(c *gin.Context) {
	var request executeRequest
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "必须确认操作预览后才能执行"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	previewService := previewservice.New(app.DB())
	operation, resourceVersion, err := previewService.Peek(c.Param("previewId"), userID)
	if err != nil {
		writeConsumeError(c, err)
		return
	}
	if err := requireOperationPermission(c, operation); err != nil {
		core.HandleError(c, err)
		return
	}
	if err := validatePreviewTarget(operation, resourceVersion); err != nil {
		writeConsumeError(c, err)
		return
	}
	operation, payload, _, _, err := previewService.Consume(c.Param("previewId"), userID)
	if err != nil {
		writeConsumeError(c, err)
		return
	}
	if err := requireServiceComponentPermission(c, operation, payload); err != nil {
		core.HandleError(c, err)
		return
	}
	result, err := executeOperation(c.Request.Context(), operation, payload, userID, auditservice.RemoteIP(c.Request))
	if err != nil {
		writeExecutionError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, result))
}

func validatePreviewTarget(operation, resourceVersion string) error {
	if strings.HasPrefix(resourceVersion, "website|") {
		parts := strings.Split(resourceVersion, "|")
		if len(parts) != 3 || !strings.HasPrefix(parts[1], "id=") || !strings.HasPrefix(parts[2], "revision=") {
			return previewservice.ErrRequestChanged
		}
		id, err := strconv.ParseInt(strings.TrimPrefix(parts[1], "id="), 10, 64)
		if err != nil || id <= 0 {
			return previewservice.ErrRequestChanged
		}
		service, err := website.DefaultService()
		if err != nil {
			return previewservice.ErrRequestChanged
		}
		current, err := service.RuntimeRevision(id)
		if err != nil || websiteTargetVersion(id, current) != resourceVersion {
			return previewservice.ErrRequestChanged
		}
		return nil
	}
	if (operation != "website.update" && operation != "website.webserver.config.update") || !strings.HasPrefix(resourceVersion, "web-server|") {
		return nil
	}
	parts := strings.Split(resourceVersion, "|")
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "path=") {
		return previewservice.ErrRequestChanged
	}
	path := strings.TrimPrefix(parts[1], "path=")
	manager, err := website.NewDefaultWebServerConfigManager()
	if err != nil {
		return previewservice.ErrRequestChanged
	}
	current, err := manager.Read(path)
	if err != nil {
		return previewservice.ErrRequestChanged
	}
	if webServerTargetVersion(path, current.Revision, manager.Server) != resourceVersion {
		return previewservice.ErrRequestChanged
	}
	return nil
}

func supportedOperation(operation string) bool {
	_, ok := accessservice.OperationPermission(operation)
	return ok
}

func requireOperationPermission(c *gin.Context, operation string) *core.AppError {
	access, exists := c.Get(middleware.ContextUserAccess)
	if !exists {
		return core.NewError(core.ErrForbidden, "权限上下文不可用")
	}
	userAccess, ok := access.(*accessservice.UserAccess)
	if !ok || userAccess == nil {
		return core.NewError(core.ErrForbidden, "权限上下文无效")
	}
	permission, supported := accessservice.OperationPermission(operation)
	if !supported || !userAccess.HasPermission(permission) {
		return core.NewError(core.ErrForbidden, "没有执行该操作的权限")
	}
	return nil
}

func requireServiceComponentPermission(c *gin.Context, operation string, payload json.RawMessage) *core.AppError {
	if operation != "software.service_action" {
		return nil
	}
	var request struct {
		Component string `json:"component"`
	}
	if err := json.Unmarshal(payload, &request); err != nil || strings.TrimSpace(request.Component) == "" {
		return core.NewError(core.ErrBadRequest, "服务控制组件参数不正确")
	}
	access, ok := middleware.UserAccess(c)
	definition, resolveErr := softwareService.ResolveServiceComponent(app.DB(), request.Component)
	if resolveErr != nil || !ok || !access.CanControlServiceScopes(definition.ManageScopes, definition.Component) {
		return core.NewError(core.ErrForbidden, "当前角色无权控制该组件服务")
	}
	return nil
}

func buildDocument(operation string, payload json.RawMessage) (previewservice.Document, string, error) {
	if _, _, err := previewservice.NormalizePayload(payload); err != nil {
		return previewservice.Document{}, "", err
	}
	document := previewservice.Document{
		Review:    previewservice.Review{Required: true, RiskLevel: "high", Reason: "该操作会改变系统状态，执行前需要确认"},
		Prechecks: []previewservice.Precheck{{Name: "实际系统状态", Status: "deferred", Message: "执行阶段将重新探测并校验"}},
		Rollback:  previewservice.Rollback{Supported: true, Summary: "执行失败时由对应业务事务或任务流程尝试恢复"},
	}
	switch operation {
	case "website.create":
		var value models.Website
		if err := json.Unmarshal(payload, &value); err != nil {
			return previewservice.Document{}, "", err
		}
		service, err := website.DefaultService()
		if err != nil {
			return previewservice.Document{}, "", err
		}
		runtime, err := service.PreviewCreate(&value)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		document, _ = websiteRuntimeDocument(operation, runtime)
		document.Files = append([]previewservice.FileChange{{Path: runtime.Website.RootDir, Action: "create_or_use_directory", ChangeSummary: "创建或复用规范化后的网站根目录"}}, document.Files...)
		document.Impact.ModifyDatabase = true
		document.Impact.ReloadService = true
		return document, "", nil
	case "website.update":
		var discriminator struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(payload, &discriminator); err != nil {
			return previewservice.Document{}, "", err
		}
		switch strings.ToLower(strings.TrimSpace(discriminator.Action)) {
		case "web_server_config":
			var request website.WebServerConfigUpdate
			if err := json.Unmarshal(payload, &request); err != nil {
				return previewservice.Document{}, "", err
			}
			manager, err := website.NewDefaultWebServerConfigManager()
			if err != nil {
				return previewservice.Document{}, "", err
			}
			current, err := manager.Read(request.Path)
			if err != nil {
				return previewservice.Document{}, "", err
			}
			if !strings.EqualFold(current.Revision, strings.TrimSpace(request.Revision)) {
				return previewservice.Document{}, "", fmt.Errorf(
					"%w: configuration changed after it was opened; reload it before saving",
					website.ErrWebServerConfigConflict,
				)
			}
			if err := manager.ValidateContent(context.Background(), request.Path, request.Content); err != nil {
				return previewservice.Document{}, "", fmt.Errorf("%w: %v", website.ErrWebServerConfigValidate, err)
			}
			document.Files = []previewservice.FileChange{{Path: current.Path, Action: "update", ChangeSummary: "更新 Web 服务器受管配置"}}
			document.Actions = webServerPreviewActions(manager.Server)
			document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
			document.Rollback = previewservice.Rollback{Supported: true, Summary: "执行前创建配置快照，校验或重载失败时恢复原配置"}
			return document, webServerTargetVersion(request.Path, current.Revision, manager.Server), nil
		case "":
			var value models.Website
			if err := json.Unmarshal(payload, &value); err != nil {
				return previewservice.Document{}, "", err
			}
			runtime, err := website.DefaultService()
			if err != nil {
				return previewservice.Document{}, "", err
			}
			change, err := runtime.PreviewWebsiteUpdate(&value)
			if err != nil {
				return previewservice.Document{}, "", err
			}
			document, version := websiteRuntimeDocument(operation, change)
			return document, version, nil
		default:
			return previewservice.Document{}, "", errUnsupportedWebsiteOperation
		}
	case "website.settings.update":
		var request websiteSettingsUpdatePayload
		if err := json.Unmarshal(payload, &request); err != nil {
			return previewservice.Document{}, "", err
		}
		id := websitePayloadID(request.ID, request.WebsiteID)
		if id <= 0 {
			return previewservice.Document{}, "", website.ErrWebsiteIDRequired
		}
		service, err := website.DefaultService()
		if err != nil {
			return previewservice.Document{}, "", err
		}
		change, err := service.PreviewSettingsUpdate(id, request.Settings)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		document, version := websiteRuntimeDocument(operation, change)
		return document, version, nil
	case "website.config.update":
		var request websiteConfigUpdatePayload
		if err := json.Unmarshal(payload, &request); err != nil {
			return previewservice.Document{}, "", err
		}
		id := websitePayloadID(request.ID, request.WebsiteID)
		if id <= 0 {
			return previewservice.Document{}, "", website.ErrWebsiteIDRequired
		}
		service, err := website.DefaultService()
		if err != nil {
			return previewservice.Document{}, "", err
		}
		change, err := service.PreviewManagedConfig(context.Background(), id, request.Content, request.Revision)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		document, version := websiteRuntimeDocument(operation, change)
		return document, version, nil
	case "website.webserver.config.update":
		var request website.WebServerConfigUpdate
		if err := json.Unmarshal(payload, &request); err != nil {
			return previewservice.Document{}, "", err
		}
		manager, err := website.NewDefaultWebServerConfigManager()
		if err != nil {
			return previewservice.Document{}, "", err
		}
		current, err := manager.Read(request.Path)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		if !strings.EqualFold(current.Revision, strings.TrimSpace(request.Revision)) {
			return previewservice.Document{}, "", fmt.Errorf("%w: configuration changed after it was opened; reload it before saving", website.ErrWebServerConfigConflict)
		}
		if err := manager.ValidateContent(context.Background(), request.Path, request.Content); err != nil {
			return previewservice.Document{}, "", fmt.Errorf("%w: %v", website.ErrWebServerConfigValidate, err)
		}
		document.Files = []previewservice.FileChange{{Path: current.Path, Action: "update", ChangeSummary: "更新 Web 服务器受管配置", Diff: boundedConfigDiff(current.Content, request.Content)}}
		document.Actions = webServerPreviewActions(manager.Server)
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "执行前创建配置快照，校验或重载失败时恢复原配置"}
		return document, webServerTargetVersion(request.Path, current.Revision, manager.Server), nil
	case "website.toggle":
		var request struct {
			ID      int64 `json:"id"`
			Enabled bool  `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &request); err != nil {
			return previewservice.Document{}, "", err
		}
		if request.ID <= 0 {
			return previewservice.Document{}, "", website.ErrWebsiteIDRequired
		}
		service, err := website.DefaultService()
		if err != nil {
			return previewservice.Document{}, "", err
		}
		change, err := service.PreviewToggle(request.ID, request.Enabled)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		document, version := websiteRuntimeDocument(operation, change)
		return document, version, nil
	case "software.install":
		document.Actions = []previewservice.Action{{Type: "component", Name: "执行受控软件安装动作", DisplayCommand: "由组件安装器按软件 key 和版本执行"}, {Type: "service", Name: "安装后验证服务状态", Service: "由组件探测器确定"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "任务失败时由软件任务执行器按组件策略回滚或保留失败现场"}
	case "software.uninstall":
		document.Actions = []previewservice.Action{{Type: "component", Name: "执行受控软件卸载动作", DisplayCommand: "由组件卸载器按软件 key 和版本执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true}
	case "software.service_action":
		document.Actions = []previewservice.Action{{Type: "service", Name: "执行组件服务动作", DisplayCommand: "systemctl <action> <component>", Service: "由组件参数确定"}}
		document.Impact = previewservice.Impact{RestartService: true}
	case "software.configure":
		document.Files = []previewservice.FileChange{{Path: "组件受管配置文件", Action: "update", ChangeSummary: "应用组件配置并保存历史"}}
		document.Actions = []previewservice.Action{{Type: "command", Name: "校验并应用组件配置", DisplayCommand: "由组件配置动作执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
	case "firewall.rule_change":
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			return previewservice.Document{}, "", err
		}
		if fields["blocked"] != nil {
			document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改防火墙 Ping 响应", DisplayCommand: "由检测到的防火墙后端执行受控 ICMP 规则动作"}}
			document.Impact = previewservice.Impact{NetworkRisk: true}
			document.Rollback = previewservice.Rollback{Supported: true, Summary: "可通过反向设置 Ping 响应状态恢复"}
		} else {
			var value struct {
				Action string              `json:"action"`
				Rule   models.IptablesRule `json:"rule"`
			}
			if err := json.Unmarshal(payload, &value); err != nil {
				return previewservice.Document{}, "", err
			}
			hasRule := hasFirewallRuleFields(fields) || fields["rule"] != nil
			if hasFirewallRuleFields(fields) {
				if err := json.Unmarshal(payload, &value.Rule); err != nil {
					return previewservice.Document{}, "", err
				}
			}
			action := strings.ToLower(strings.TrimSpace(value.Action))
			if action == "create" {
				action = "add"
			}
			if action == "add" || action == "update" || (action == "" && hasRule) {
				if err := safeservice.NewDefaultService().ValidateRule(&value.Rule); err != nil {
					return previewservice.Document{}, "", err
				}
			}
			document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改防火墙规则", DisplayCommand: "由检测到的防火墙后端执行受控规则动作"}}
			document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
			document.Rollback = previewservice.Rollback{Supported: true, Summary: "失败时恢复已应用的规则操作和持久化状态"}
		}
	case "firewall.port_forward":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改端口转发", DisplayCommand: "由检测到的防火墙后端执行受控转发动作"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
	case "firewall.toggle":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "切换防火墙状态", DisplayCommand: "由检测到的防火墙后端执行启停动作"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: false, Summary: "防火墙启停可能导致当前连接中断，请确认后执行", Unrecoverable: []string{"已断开的外部连接"}}
	case "firewall.ping":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改防火墙 Ping 响应", DisplayCommand: "由检测到的防火墙后端执行受控 ICMP 规则动作"}}
		document.Impact = previewservice.Impact{NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "可通过反向设置 Ping 响应状态恢复"}
	case "panel.network":
		document.Files = []previewservice.FileChange{{Path: "面板受管配置文件", Action: "update", ChangeSummary: "更新面板监听、TLS、安全入口和可信代理配置"}}
		document.Actions = []previewservice.Action{{Type: "system", Name: "应用面板访问配置", DisplayCommand: "由面板网络配置事务执行"}, {Type: "firewall", Name: "同步面板端口规则", DisplayCommand: "由受控防火墙适配器执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "由面板网络事务恢复配置文件和已准备的端口规则"}
	case "fail2ban.policy_change":
		var value fail2banservice.PolicyChangeRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return previewservice.Document{}, "", err
		}
		document.Files = []previewservice.FileChange{{Path: "Fail2ban 的 OneinStack 受管规则文件", Action: "create_update_or_delete", ChangeSummary: "原子应用固定模板规则"}}
		document.Actions = []previewservice.Action{{Type: "command", Name: "校验 Fail2ban 配置", DisplayCommand: "fail2ban-client -t"}, {Type: "service", Name: "重新加载 Fail2ban", DisplayCommand: "fail2ban-client reload", Service: "fail2ban"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true, NetworkRisk: value.Policy.Enabled && value.Policy.EnforcementMode == "autoBan"}
		return document, value.Policy.BaseRevision, nil
	case "fail2ban.ban", "fail2ban.unban":
		var value fail2banservice.BanRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return previewservice.Document{}, "", err
		}
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "通过受管 Fail2ban jail 处置单个 IP", DisplayCommand: "fail2ban-client set <managed-jail> banip|unbanip <ip>"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "可通过对应的解封或重新封禁任务恢复"}
	}
	return document, "", nil
}

func normalizeWebsiteCreatePayload(payload json.RawMessage) (json.RawMessage, error) {
	var value models.Website
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	service, err := website.DefaultService()
	if err != nil {
		return nil, err
	}
	normalized, err := service.PrepareCreate(&value)
	if err != nil {
		if errors.Is(err, website.ErrWebsiteRootInvalid) {
			return nil, err
		}
		return nil, fmt.Errorf("validate website create payload: %w", err)
	}
	result, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("normalize website create payload: %w", err)
	}
	return result, nil
}

func executeOperation(ctx context.Context, operation string, payload json.RawMessage, userID int64, requestIP string) (gin.H, error) {
	switch operation {
	case "website.create":
		var value models.Website
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		if err := service.AddPrepared(ctx, &value); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "resourceId": value.ID, "status": "succeeded"}, nil
	case "website.update":
		var discriminator struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(payload, &discriminator); err != nil {
			return nil, err
		}
		if strings.EqualFold(strings.TrimSpace(discriminator.Action), "web_server_config") {
			var request website.WebServerConfigUpdate
			if err := json.Unmarshal(payload, &request); err != nil {
				return nil, err
			}
			return executeWebServerConfigUpdate(ctx, operation, request, userID, requestIP)
		}
		if strings.TrimSpace(discriminator.Action) != "" {
			return nil, errUnsupportedWebsiteOperation
		}
		var value models.Website
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		before, err := service.Get(value.ID)
		if err != nil {
			return nil, err
		}
		if err := service.EnsureWebsiteOwnerActive(before); err != nil {
			return nil, err
		}
		beforeConfig, _ := service.ReadManagedConfig(ctx, value.ID)
		snapshot, err := createWebsiteOperationSnapshot("update", value.ID, before, value, []byte(beforeConfig.Content), "website-before.conf", userID)
		if err != nil {
			return nil, err
		}
		if err := service.Update(ctx, &value); err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			recordWebsiteOperationAudit(snapshot.ID, "failed", err.Error(), userID, requestIP)
			return nil, err
		}
		if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, value, "succeeded", ""); err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			return nil, err
		}
		recordWebsiteOperationAudit(snapshot.ID, "succeeded", "网站基础配置已发布", userID, requestIP)
		return gin.H{"operation": operation, "resourceId": value.ID, "status": "succeeded", "snapshotId": snapshot.ID}, nil
	case "website.settings.update":
		var request websiteSettingsUpdatePayload
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		id := websitePayloadID(request.ID, request.WebsiteID)
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		before, err := service.GetSettings(id)
		if err != nil {
			return nil, err
		}
		if err := service.EnsureWebsiteOwnerActive(&before.Website); err != nil {
			return nil, err
		}
		beforeJSON, err := json.Marshal(before)
		if err != nil {
			return nil, err
		}
		snapshot, err := createWebsiteOperationSnapshot("settings.update", id, before.Settings, request.Settings, beforeJSON, "website-before.json", userID)
		if err != nil {
			return nil, err
		}
		document, err := service.UpdateSettings(ctx, id, request.Settings)
		if err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			recordWebsiteOperationAudit(snapshot.ID, "failed", err.Error(), userID, requestIP)
			return nil, err
		}
		if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, document.Settings, "succeeded", ""); err != nil {
			return nil, err
		}
		recordWebsiteOperationAudit(snapshot.ID, "succeeded", "网站结构化配置已发布", userID, requestIP)
		return gin.H{"operation": operation, "resourceId": id, "status": "succeeded", "snapshotId": snapshot.ID, "website": document}, nil
	case "website.config.update":
		var request websiteConfigUpdatePayload
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		id := websitePayloadID(request.ID, request.WebsiteID)
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		beforeSite, err := service.Get(id)
		if err != nil {
			return nil, err
		}
		if err := service.EnsureWebsiteOwnerActive(beforeSite); err != nil {
			return nil, err
		}
		before, err := service.ReadManagedConfig(ctx, id)
		if err != nil {
			return nil, err
		}
		snapshot, err := createWebsiteOperationSnapshot("config.update", id, before, request, []byte(before.Content), "website-config-before.conf", userID)
		if err != nil {
			return nil, err
		}
		result, err := service.UpdateManagedConfig(ctx, id, request.Content, request.Revision)
		if err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			recordWebsiteOperationAudit(snapshot.ID, "failed", err.Error(), userID, requestIP)
			return nil, err
		}
		if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, result, "succeeded", ""); err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			return nil, err
		}
		recordWebsiteOperationAudit(snapshot.ID, "succeeded", "网站受管配置已发布", userID, requestIP)
		return gin.H{"operation": operation, "resourceId": id, "status": "succeeded", "snapshotId": snapshot.ID, "config": result}, nil
	case "website.webserver.config.update":
		var request website.WebServerConfigUpdate
		if err := json.Unmarshal(payload, &request); err != nil {
			return nil, err
		}
		return executeWebServerConfigUpdate(ctx, operation, request, userID, requestIP)
	case "website.toggle":
		var value input.WebsiteStatusParam
		var raw struct {
			ID      int64 `json:"id"`
			Enabled bool  `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &raw); err != nil {
			return nil, err
		}
		value.Enabled = raw.Enabled
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		before, err := service.Get(raw.ID)
		if err != nil {
			return nil, err
		}
		if err := service.EnsureWebsiteOwnerActive(before); err != nil {
			return nil, err
		}
		snapshot, err := createWebsiteOperationSnapshot("toggle", raw.ID, before, raw, nil, "", userID)
		if err != nil {
			return nil, err
		}
		site, err := service.SetEnabled(ctx, raw.ID, value.Enabled)
		if err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			recordWebsiteOperationAudit(snapshot.ID, "failed", err.Error(), userID, requestIP)
			return nil, err
		}
		if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, site, "succeeded", ""); err != nil {
			_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
			return nil, err
		}
		recordWebsiteOperationAudit(snapshot.ID, "succeeded", "网站状态已发布", userID, requestIP)
		return gin.H{"operation": operation, "resourceId": site.ID, "status": "succeeded", "snapshotId": snapshot.ID}, nil
	case "software.install":
		var value input.InstallParams
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		task, err := software.SubmitInstallationTask(value, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.uninstall":
		var value input.RemoveParams
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitUninstall(value.Name, value.Version, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.service_action":
		var value struct {
			Component string `json:"component"`
			Action    string `json:"action"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitServiceAction(value.Component, value.Action, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.configure":
		var value struct {
			Component string            `json:"component"`
			Revision  string            `json:"revision"`
			Values    map[string]string `json:"values"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		definition, err := softwareService.NormalizeServiceComponent(value.Component)
		if err != nil {
			return nil, err
		}
		var installed models.Software
		if err := app.DB().Where("`key` = ? AND installed = ?", definition.SoftwareKey, true).Order("install_time DESC").First(&installed).Error; err != nil {
			return nil, err
		}
		version := strings.TrimSpace(installed.InstallVersion)
		if version == "" {
			version = strings.TrimSpace(installed.Version)
		}
		if version == "" {
			return nil, errors.New("component install version is missing")
		}
		current, err := softwareService.NewInstaller().InspectServiceConfiguration(ctx, definition.Component, version)
		if err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitConfiguration(definition.Component, value.Revision, current.Values, value.Values, "", userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "firewall.rule_change":
		var value struct {
			Action  string              `json:"action"`
			Rule    models.IptablesRule `json:"rule"`
			ID      int64               `json:"id"`
			Enabled *bool               `json:"enabled"`
			Blocked *bool               `json:"blocked"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(payload, &fields); err != nil {
			return nil, err
		}
		directRule := hasFirewallRuleFields(fields)
		if directRule {
			if err := json.Unmarshal(payload, &value.Rule); err != nil {
				return nil, err
			}
		}
		service := safeservice.NewDefaultService()
		var err error
		action := strings.ToLower(strings.TrimSpace(value.Action))
		switch action {
		case "create":
			action = "add"
		case "set_ping":
			action = "ping"
		}
		if action == "" {
			switch {
			case value.Blocked != nil:
				action = "ping"
			case value.Enabled != nil && value.ID > 0:
				action = "state"
			case directRule || fields["rule"] != nil:
				if value.Rule.ID > 0 {
					action = "update"
				} else {
					action = "add"
				}
			case value.ID > 0:
				action = "delete"
			}
		}
		switch action {
		case "add":
			err = service.Add(ctx, &value.Rule)
		case "update":
			err = service.Update(ctx, &value.Rule)
		case "delete":
			err = service.Delete(ctx, value.ID)
		case "state":
			if value.Enabled == nil {
				return nil, errors.New("enabled is required")
			}
			err = service.SetRuleState(ctx, value.ID, *value.Enabled)
		case "ping", "block_ping", "blockping":
			if value.Blocked == nil {
				return nil, errors.New("blocked is required")
			}
			err = service.SetPingBlocked(ctx, *value.Blocked)
		default:
			return nil, errors.New("无法识别防火墙规则操作，请提供 action 或有效的规则参数")
		}
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "firewall.port_forward":
		var value struct {
			Action  string                     `json:"action"`
			Forward models.FirewallPortForward `json:"forward"`
			ID      int64                      `json:"id"`
			Enabled *bool                      `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service := safeservice.NewDefaultService()
		var err error
		switch strings.ToLower(value.Action) {
		case "add":
			err = service.AddPortForward(ctx, &value.Forward)
		case "update":
			err = service.UpdatePortForward(ctx, &value.Forward)
		case "delete":
			err = service.DeletePortForward(ctx, value.ID)
		case "state":
			if value.Enabled == nil {
				return nil, errors.New("enabled is required")
			}
			err = service.SetPortForwardState(ctx, value.ID, *value.Enabled)
		default:
			return nil, errors.New("unsupported port forward action")
		}
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "firewall.toggle":
		var value input.FirewallToggleParam
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		if !value.Enabled && strings.TrimSpace(value.Confirm) == "" {
			value.Confirm = safeservice.DisableConfirmation
		}
		if err := safeservice.NewDefaultService().SetEnabled(ctx, value.Enabled, value.Confirm); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "firewall.ping":
		var value input.FirewallPingParam
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		if err := safeservice.NewDefaultService().SetPingBlocked(ctx, value.Blocked); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "panel.network":
		var value systemservice.UpdatePanelNetworkRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		settings, err := systemservice.UpdatePanelNetwork(value)
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "settings": settings, "status": "succeeded"}, nil
	case "fail2ban.policy_change":
		var value fail2banservice.PolicyChangeRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		task, err := fail2banservice.DefaultManager().SubmitPolicyChange(value, userID, requestIP, "user")
		if err != nil {
			return nil, err
		}
		return gin.H(fail2banservice.TaskResult(task)), nil
	case "fail2ban.ban", "fail2ban.unban":
		var value fail2banservice.BanRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		taskOperation := "ban_ip"
		if operation == "fail2ban.unban" {
			taskOperation = "unban_ip"
		}
		task, err := fail2banservice.DefaultManager().SubmitBan(taskOperation, value, userID, requestIP, "user")
		if err != nil {
			return nil, err
		}
		return gin.H(fail2banservice.TaskResult(task)), nil
	default:
		return nil, fmt.Errorf("unsupported operation %s", operation)
	}
}

func executeWebServerConfigUpdate(
	ctx context.Context,
	operation string,
	request website.WebServerConfigUpdate,
	userID int64,
	requestIP string,
) (gin.H, error) {
	manager, err := website.NewDefaultWebServerConfigManager()
	if err != nil {
		return nil, err
	}
	before, err := manager.Read(request.Path)
	if err != nil {
		return nil, err
	}
	snapshotService := configsnapshot.Default()
	snapshot, err := snapshotService.Create(configsnapshot.CreateInput{
		ResourceType: "nginx",
		ResourceID:   request.Path,
		Operation:    "update",
		Before:       before,
		After:        request,
		RequestedBy:  userID,
		Artifact:     []byte(before.Content),
		ArtifactName: "nginx-before.conf",
	})
	if err != nil {
		return nil, err
	}
	result, err := manager.Update(ctx, request)
	if err != nil {
		_ = snapshotService.Mark(snapshot.ID, "failed", err.Error())
		recordWebServerConfigAudit(snapshot.ID, request.Path, "failed", err.Error(), userID, requestIP)
		return nil, err
	}
	if err := snapshotService.MarkWithAfter(snapshot.ID, result, "succeeded", ""); err != nil {
		_ = snapshotService.Mark(snapshot.ID, "failed", err.Error())
		return nil, err
	}
	recordWebServerConfigAudit(snapshot.ID, request.Path, "succeeded", "Nginx 受管配置已发布", userID, requestIP)
	return gin.H{
		"operation":  operation,
		"status":     "succeeded",
		"snapshotId": snapshot.ID,
		"config":     result,
	}, nil
}

func createWebsiteOperationSnapshot(operation string, websiteID int64, before, after any, artifact []byte, artifactName string, userID int64) (*models.ConfigurationSnapshot, error) {
	return configsnapshot.Default().Create(configsnapshot.CreateInput{
		ResourceType: "website",
		ResourceID:   fmt.Sprint(websiteID),
		Operation:    operation,
		Before:       before,
		After:        after,
		RequestedBy:  userID,
		Artifact:     artifact,
		ArtifactName: artifactName,
	})
}

func recordWebsiteOperationAudit(snapshotID, status, message string, userID int64, requestIP string) {
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	outcome := "success"
	statusCode := http.StatusAccepted
	if status == "failed" {
		outcome = "failure"
		statusCode = http.StatusInternalServerError
	}
	_, _ = manager.Append(auditservice.EventInput{
		EventType: "configuration",
		Action:    "website.operation.preview_execute",
		Method:    http.MethodPost,
		Route:     "/v1/operations/:previewId/execute",
		Path:      "/v1/operations/execute",
		Status:    statusCode,
		Outcome:   outcome,
		Sensitive: true,
		UserID:    userID,
		RemoteIP:  requestIP,
		Message:   fmt.Sprintf("snapshot=%s status=%s %s", snapshotID, status, strings.TrimSpace(message)),
	})
}

func webServerTargetVersion(path, revision string, server website.WebServerInfo) string {
	return fmt.Sprintf(
		"web-server|path=%s|binary=%s|prefix=%s|config=%s|main=%s|revision=%s",
		path,
		server.BinaryPath,
		server.Prefix,
		server.ConfigRoot,
		server.MainConfigPath,
		revision,
	)
}

func recordWebServerConfigAudit(snapshotID, path, status, message string, userID int64, requestIP string) {
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	outcome := "success"
	httpStatus := http.StatusAccepted
	if status == "failed" {
		outcome = "failure"
		httpStatus = http.StatusInternalServerError
	}
	_, _ = manager.Append(auditservice.EventInput{
		EventType: "configuration",
		Action:    "config.snapshot.update",
		Method:    http.MethodPost,
		Route:     "/v1/operations/:previewId/execute",
		Path:      "/v1/operations/execute",
		Status:    httpStatus,
		Outcome:   outcome,
		Sensitive: true,
		UserID:    userID,
		RemoteIP:  requestIP,
		Message: fmt.Sprintf(
			"snapshot=%s resource=nginx/%s status=%s %s",
			snapshotID,
			path,
			status,
			strings.TrimSpace(message),
		),
	})
}

func hasFirewallRuleFields(fields map[string]json.RawMessage) bool {
	for _, name := range []string{"ruleType", "direction", "protocol", "strategy", "ips", "ports", "state", "remark", "location", "expiresAt"} {
		if _, ok := fields[name]; ok {
			return true
		}
	}
	return false
}

func softwareTaskResult(task *models.SoftwareTask) gin.H {
	return gin.H{"taskId": task.ID, "operation": task.Operation, "component": task.Component, "status": task.Status, "statusUrl": "/v1/soft/tasks/" + task.ID, "streamUrl": "/v1/soft/tasks/" + task.ID + "/events"}
}

func writeConsumeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, previewservice.ErrNotFound), errors.Is(err, previewservice.ErrExpired), errors.Is(err, previewservice.ErrConsumed), errors.Is(err, previewservice.ErrRequestChanged):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "操作预览已失效，请重新预览"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取操作预览失败"))
	}
}

func writeExecutionError(c *gin.Context, err error) {
	code, message := core.ErrConfigError, "执行已确认的操作预览失败"
	detail := err.Error()
	var applyErr *website.WebServerConfigApplyError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code, message = core.ErrTaskTimeout, "操作超时，原配置已尝试恢复"
		detail = "Web Server 配置校验或重载未在限定时间内完成，请检查服务状态和日志后重试。"
	case errors.Is(err, context.Canceled):
		code, message = core.ErrTaskCanceled, "操作已取消，原配置已尝试恢复"
		detail = "当前请求已取消，操作未完成；请确认服务状态后重新预览并执行。"
	case errors.Is(err, website.ErrWebServerConfigConflict):
		code, message = core.ErrConflict, "配置已发生变化，请重新预览后再执行"
		detail = ""
	case errors.Is(err, website.ErrWebServerConfigValidate):
		code, message = core.ErrConfigValidateFailed, "Web Server 配置校验失败，原配置已恢复"
	case errors.Is(err, website.ErrWebsiteSettingsValidate):
		code, message = core.ErrInvalidParameter, "网站设置格式错误"
		detail = ""
	case errors.Is(err, website.ErrWebsiteRootInvalid):
		code, message = core.ErrInvalidParameter, "网站根目录无效，请填写受管网站根目录下的相对目录"
		detail = ""
	case errors.Is(err, website.ErrWebsiteParameterInvalid):
		code, message = core.ErrInvalidParameter, "网站参数无效"
		detail = ""
	case errors.Is(err, website.ErrWebsiteIDRequired):
		code, message = core.ErrInvalidParameter, "网站操作缺少 websiteId 或 id"
		detail = ""
	case errors.Is(err, website.ErrWebsiteWebServerMismatch):
		code, message = core.ErrResourceStateInvalid, "网站归属 Web Server 不一致"
	case errors.Is(err, website.ErrWebsiteEngineImmutable):
		code, message = core.ErrInvalidParameter, "网站归属 Web Server 不可修改"
	case errors.Is(err, website.ErrWebsiteConfigUnavailable):
		code, message = core.ErrResourceStateInvalid, "网站运行配置不可用"
	case errors.Is(err, website.ErrWebsiteConflict):
		code, message = core.ErrConflict, "网站已存在，请检查网站名称或域名"
		detail = ""
	case errors.Is(err, website.ErrWebsiteExpired):
		code, message = core.ErrResourceStateInvalid, "网站已到期，请先修改到期时间后再启用"
		detail = ""
	case errors.Is(err, gorm.ErrRecordNotFound):
		code, message = core.ErrWebsiteNotFound, "网站不存在或已被删除，请刷新后重试"
		detail = ""
	case errors.Is(err, website.ErrWebServerUnavailable):
		code, message = core.ErrConfigError, "未检测到可管理的 Web Server"
		detail = ""
	case errors.Is(err, errUnsupportedWebsiteOperation):
		code, message = core.ErrBadRequest, "不支持的网站更新操作"
		detail = ""
	case errors.As(err, &applyErr) && applyErr.Status == website.WebServerConfigApplyStatusReloadFailedRolled:
		code, message = core.ErrConfigApplyFailed, "Web Server 重载失败，原配置已回滚"
	case errors.Is(err, fail2banservice.ErrValidation):
		code, message = core.ErrValidationFailed, "入侵防护参数无效，请检查后重试"
	case errors.Is(err, fail2banservice.ErrRevisionConflict):
		code, message = core.ErrResourceStateInvalid, "规则已被其他操作修改，请刷新后重试"
	case errors.Is(err, fail2banservice.ErrProtectedAddress):
		code, message = core.ErrInsufficientPermissions, "该地址属于系统保护范围，不能封禁"
	case errors.Is(err, fail2banservice.ErrUnavailable):
		code, message = core.ErrServiceUnavailable, "Fail2ban 未安装、未验证或服务不可用"
	}
	if detail == "" {
		core.HandleError(c, core.NewError(code, message))
		return
	}
	core.HandleError(c, core.NewErrorWithDetail(code, message, detail))
}
