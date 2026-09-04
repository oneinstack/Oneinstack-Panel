package operationpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"path/filepath"
	"sort"
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

type softwareConfigurationPayload struct {
	Component          string            `json:"component"`
	Revision           string            `json:"revision"`
	Values             map[string]string `json:"values"`
	RestoreFromID      string            `json:"restoreFromId,omitempty"`
	RestoreFromHistory string            `json:"restoreFromHistoryId,omitempty"`
}

var (
	errSoftwareConfigurationComponentRequired = errors.New("software configuration component is required")
	errSoftwareConfigurationUnsupported       = errors.New("software configuration component is unsupported")
	errSoftwareConfigurationDatabase          = errors.New("software configuration database is unavailable")
	errSoftwareConfigurationNotInstalled      = errors.New("software configuration component is not installed")
	errSoftwareConfigurationVersionMissing    = errors.New("software configuration install version is missing")
	errSoftwareConfigurationCurrentRead       = errors.New("software configuration current state cannot be read")
	errSoftwareConfigurationHistoryNotFound   = errors.New("software configuration history does not exist")
	errSoftwareConfigurationHistoryRead       = errors.New("software configuration history cannot be read")
	errSoftwareConfigurationHistoryState      = errors.New("software configuration history is not successfully published")
	errSoftwareConfigurationRestoreIDs        = errors.New("software configuration restore source IDs do not match")
	errSoftwareConfigurationRestoreValues     = errors.New("software configuration history restore has conflicting fields")
	errSoftwareConfigurationNoChanges         = errors.New("software configuration has no changes")
)

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
	if runtime.AccessLogPath != "" && strings.EqualFold(runtime.Website.Engine, "caddy") {
		document.Impact.WriteFiles = true
		document.Files = append(document.Files, previewservice.FileChange{
			Path: runtime.AccessLogPath, Action: "create_or_use_file", ChangeSummary: "创建或复用 Caddy 访问日志文件并授予运行用户写入权限",
		})
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

const fail2banProtectedAddressDetail = "目标地址属于私网、回环、链路本地、可信代理、Panel 监听地址或当前请求来源 IP，不能封禁。"

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
	if operation == "software.configure" {
		var err error
		payload, err = normalizeSoftwareConfigurePayload(c.Request.Context(), payload)
		if err != nil {
			handleSoftwareConfigurationPreviewError(c, err)
			return
		}
	}
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
	if operation == "fail2ban.ban" || operation == "fail2ban.unban" {
		var err error
		payload, err = normalizeFail2banBanPayload(payload, operation, c.ClientIP())
		if err != nil {
			if handleFail2banPreviewError(c, err) {
				return
			}
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "入侵防护预览参数无效"))
			return
		}
	}
	document, resourceVersion, err := buildDocument(c.Request.Context(), operation, payload)
	if err != nil {
		var parameterErr *softwareService.InstallParameterError
		if errors.As(err, &parameterErr) {
			if userMessage := parameterErr.UserMessage(); userMessage != "" {
				core.HandleSimpleError(c, core.NewError(core.ErrInvalidParameter, userMessage))
				return
			}
			appErr := core.NewErrorWithDetail(core.ErrInvalidParameter, "软件安装参数无效", parameterErr.Error())
			appErr.Field = parameterErr.Field
			core.HandleError(c, appErr)
			return
		}
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

func normalizeFail2banBanPayload(payload json.RawMessage, operation, requestIP string) (json.RawMessage, error) {
	var request fail2banservice.BanRequest
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	request.RequestIP = requestIP
	var err error
	if operation == "fail2ban.unban" {
		request, _, _, err = fail2banservice.DefaultService().ResolveUnbanRequest(request)
	} else {
		request, _, _, err = fail2banservice.DefaultService().ResolveBanRequest(request)
	}
	if err != nil {
		return nil, err
	}
	result, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("normalize fail2ban ban payload: %w", err)
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
	case errors.Is(err, fail2banservice.ErrProtectedAddress):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInsufficientPermissions,
			"该地址属于系统保护范围，不能封禁",
			fail2banProtectedAddressDetail,
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
	if err := validatePreviewTarget(c.Request.Context(), operation, resourceVersion); err != nil {
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

func inspectSoftwareConfigurationTarget(
	ctx context.Context,
	component string,
) (softwareService.ComponentServiceDefinition, string, softwareService.ComponentConfiguration, error) {
	database := app.DB()
	if database == nil {
		return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, errors.New("database is not initialized")
	}
	definition, err := softwareService.ResolveServiceComponent(database, component)
	if err != nil {
		return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, err
	}
	var installed models.Software
	if err := database.
		Where("installed = ?", true).
		Where("(`key` = ? OR `component` = ?)", definition.SoftwareKey, definition.Component).
		Order("install_time DESC").
		First(&installed).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, errors.New("component is not installed")
		}
		return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, err
	}
	version := strings.TrimSpace(installed.InstallVersion)
	if version == "" {
		version = strings.TrimSpace(installed.Version)
	}
	if version == "" {
		return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, errors.New("component install version is missing")
	}
	configuration, err := softwareService.NewInstaller().InspectServiceConfiguration(ctx, definition.Component, version)
	if err != nil {
		return softwareService.ComponentServiceDefinition{}, "", softwareService.ComponentConfiguration{}, err
	}
	return definition, version, configuration, nil
}

func normalizeSoftwareConfigurePayload(ctx context.Context, payload json.RawMessage) (json.RawMessage, error) {
	var request softwareConfigurationPayload
	if err := json.Unmarshal(payload, &request); err != nil {
		return nil, err
	}
	request.Component = strings.TrimSpace(request.Component)
	if request.Component == "" {
		return nil, errSoftwareConfigurationComponentRequired
	}
	restoreFromID := strings.TrimSpace(request.RestoreFromID)
	restoreFromHistoryID := strings.TrimSpace(request.RestoreFromHistory)
	if restoreFromID != "" && restoreFromHistoryID != "" && restoreFromID != restoreFromHistoryID {
		return nil, errSoftwareConfigurationRestoreIDs
	}
	if restoreFromID == "" {
		restoreFromID = restoreFromHistoryID
	}
	definition, _, current, err := inspectSoftwareConfigurationTarget(ctx, request.Component)
	if err != nil {
		if strings.Contains(err.Error(), "unsupported component service") ||
			strings.Contains(err.Error(), "does not support managed configuration") {
			return nil, errSoftwareConfigurationUnsupported
		}
		switch err.Error() {
		case "database is not initialized":
			return nil, errSoftwareConfigurationDatabase
		case "component is not installed":
			return nil, errSoftwareConfigurationNotInstalled
		case "component install version is missing":
			return nil, errSoftwareConfigurationVersionMissing
		}
		return nil, fmt.Errorf("%w: %v", errSoftwareConfigurationCurrentRead, err)
	}
	if restoreFromID != "" {
		if strings.TrimSpace(request.Revision) != "" || len(request.Values) != 0 {
			return nil, errSoftwareConfigurationRestoreValues
		}
		history, err := softwareService.GetConfigurationHistory(app.DB(), definition.Component, restoreFromID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errSoftwareConfigurationHistoryNotFound
			}
			return nil, fmt.Errorf("%w: %v", errSoftwareConfigurationHistoryRead, err)
		}
		if history.Status != models.SoftwareConfigurationStatusSucceeded {
			return nil, errSoftwareConfigurationHistoryState
		}
		request.Revision = current.Revision
		request.Values = history.Before
	}
	preview, err := softwareService.PreviewConfigurationWithContext(
		ctx, current, request.Revision, request.Values,
	)
	if err != nil {
		return nil, err
	}
	if !preview.HasChanges {
		return nil, errSoftwareConfigurationNoChanges
	}
	normalized, err := json.Marshal(softwareConfigurationPayload{
		Component:     definition.Component,
		Revision:      preview.Revision,
		Values:        preview.Values,
		RestoreFromID: restoreFromID,
	})
	if err != nil {
		return nil, fmt.Errorf("encode software configuration payload: %w", err)
	}
	return normalized, nil
}

func handleSoftwareConfigurationPreviewError(c *gin.Context, err error) {
	var parameterErr *softwareService.InstallParameterError
	if errors.As(err, &parameterErr) {
		message := softwareConfigurationParameterMessage(parameterErr)
		writeSoftwareConfigurationPreviewError(c, core.ErrInvalidParameter, message, parameterErr.Field)
		return
	}

	code := core.ErrBadRequest
	field := ""
	message := ""
	switch {
	case errors.Is(err, errSoftwareConfigurationComponentRequired):
		code = core.ErrInvalidParameter
		message = "组件配置预览缺少 component 字段，请提供组件标识后重试。"
	case errors.Is(err, errSoftwareConfigurationUnsupported):
		code = core.ErrInvalidParameter
		message = "当前组件不支持受管配置预览，请选择支持配置管理的已安装组件后重试。"
	case errors.Is(err, errSoftwareConfigurationDatabase):
		code = core.ErrConfigReadFailed
		message = "面板数据库尚未初始化，无法读取组件配置；请检查面板启动状态和数据库配置后重试。"
	case errors.Is(err, errSoftwareConfigurationNotInstalled):
		code = core.ErrSoftwareNotFound
		message = "当前组件未安装，无法读取受管配置；请先安装组件并确认软件状态后重试。"
	case errors.Is(err, errSoftwareConfigurationVersionMissing):
		code = core.ErrConfigReadFailed
		message = "当前组件缺少已安装版本信息，无法确定配置脚本；请刷新软件状态或重新同步安装信息后重试。"
	case errors.Is(err, errSoftwareConfigurationRestoreIDs):
		code = core.ErrInvalidParameter
		message = "组件配置预览同时收到两个不一致的历史配置标识，请只保留一个标识，或确保两个标识完全一致后重试。"
	case errors.Is(err, errSoftwareConfigurationRestoreValues):
		code = core.ErrInvalidParameter
		message = "恢复历史组件配置时不能同时提交 revision 或 values，请仅提交 component 与历史配置标识。"
	case errors.Is(err, errSoftwareConfigurationCurrentRead):
		code = core.ErrConfigReadFailed
		message = "无法读取当前组件配置，配置读取脚本或组件运行状态异常；请确认组件服务、配置脚本和运行权限后重试。"
	case errors.Is(err, errSoftwareConfigurationHistoryNotFound):
		code = core.ErrNotFound
		message = "要恢复的配置历史不存在、已被删除或不属于当前组件，请刷新配置历史后重新选择。"
	case errors.Is(err, errSoftwareConfigurationHistoryRead):
		code = core.ErrConfigReadFailed
		message = "读取配置历史失败，历史记录可能暂时不可用；请刷新配置历史后重试。"
	case errors.Is(err, errSoftwareConfigurationHistoryState):
		code = core.ErrResourceStateInvalid
		message = "只能恢复已成功发布的配置历史，当前记录尚未成功发布；请刷新列表后选择“已发布”记录。"
	case errors.Is(err, errSoftwareConfigurationNoChanges):
		message = "当前组件配置已与所选历史记录的发布前内容一致，无需恢复；请刷新历史后选择确实不同的记录。"
	case errors.Is(err, softwareService.ErrConfigurationConflict):
		code = core.ErrConflict
		message = "当前组件配置版本已发生变化，当前请求基于旧版本；请重新读取当前配置后再预览。"
	case isSoftwareConfigurationJSONError(err):
		code = core.ErrInvalidParameter
		message = "组件配置预览请求格式不正确，请检查 component、revision、values 和历史配置标识的类型与格式后重试。"
	default:
		message, field = softwareConfigurationValidationMessage(err)
		if message != "" {
			code = core.ErrInvalidParameter
		} else {
			message = "组件配置预览校验失败，请检查当前配置版本、字段取值和组件状态后重试。"
		}
	}
	writeSoftwareConfigurationPreviewError(c, code, message, field)
}

func writeSoftwareConfigurationPreviewError(c *gin.Context, code core.ErrorCode, message, field string) {
	appErr := core.NewError(code, message)
	appErr.Field = strings.TrimSpace(field)
	core.HandleSimpleError(c, appErr)
}

func isSoftwareConfigurationJSONError(err error) bool {
	return isJSONDecodeError(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func softwareConfigurationParameterMessage(err *softwareService.InstallParameterError) string {
	if message := err.UserMessage(); message != "" {
		return message
	}
	field := strings.TrimSpace(err.Field)
	switch strings.TrimSpace(err.Message) {
	case "must be a valid port between 1 and 65535":
		return fmt.Sprintf("组件配置字段 %s 必须是 1 到 65535 之间的有效端口，请修正后重试。", field)
	case "is required":
		return fmt.Sprintf("组件配置字段 %s 未填写，请补充该字段后重试。", field)
	default:
		if field != "" {
			return fmt.Sprintf("组件配置字段 %s 参数无效，请检查类型、格式和取值范围后重试。", field)
		}
		return "组件配置参数无效，请检查类型、格式和取值范围后重试。"
	}
}

func softwareConfigurationValidationMessage(err error) (string, string) {
	text := strings.TrimSpace(err.Error())
	switch text {
	case "configuration must contain every managed field and no unknown fields":
		return "组件配置字段集合包含未知字段，请按照当前配置定义提交 values；缺少默认值的字段仍需填写。", ""
	case "workerProcesses must be auto or an integer from 1 to 99":
		return "组件配置字段 workerProcesses 必须是 auto 或 1 到 99 之间的整数，请修正后重试。", "workerProcesses"
	case "postMaxSize must be greater than or equal to uploadMaxFilesize":
		return "组件配置字段 postMaxSize 必须大于或等于 uploadMaxFilesize，请调整两个值的关系后重试。", "postMaxSize"
	case "PHP-FPM process counts must satisfy min spare ≤ start ≤ max spare ≤ max children":
		return "PHP-FPM 进程数必须满足 min spare ≤ start ≤ max spare ≤ max children，请调整相关参数后重试。", ""
	}

	const prefix = "configuration field "
	if !strings.HasPrefix(text, prefix) {
		return "", ""
	}
	rest := strings.TrimPrefix(text, prefix)
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", ""
	}
	field, reason := parts[0], parts[1]
	switch reason {
	case "is required":
		return fmt.Sprintf("组件配置字段 %s 未填写，请补充该字段后重试。", field), field
	case "contains invalid data":
		return fmt.Sprintf("组件配置字段 %s 包含不允许的内容（空字节、换行或长度超过 128 个字符），请修正后重试。", field), field
	case "is outside the allowed range":
		return fmt.Sprintf("组件配置字段 %s 的数值超出当前版本允许范围，请按照页面显示的最小值和最大值填写后重试。", field), field
	case "must be true or false":
		return fmt.Sprintf("组件配置字段 %s 只能填写 true 或 false，请修正后重试。", field), field
	case "has an unsupported value":
		return fmt.Sprintf("组件配置字段 %s 的取值不受当前组件版本支持，请从当前版本提供的选项中选择后重试。", field), field
	case "must be a normalized absolute path":
		return fmt.Sprintf("组件配置字段 %s 必须是规范化的绝对路径，请使用以 / 开头且不含 .. 的路径后重试。", field), field
	case "is too broad":
		return fmt.Sprintf("组件配置字段 %s 不能使用过于宽泛的系统目录，请指定更具体的目录后重试。", field), field
	case "has an unsupported type":
		return fmt.Sprintf("组件配置字段 %s 使用了不受支持的字段类型，请刷新配置定义后重试。", field), field
	default:
		return "", ""
	}
}

func softwareConfigurationTargetVersion(component, revision string) string {
	return fmt.Sprintf("software-config|component=%s|revision=%s", component, revision)
}

func validatePreviewTarget(ctx context.Context, operation, resourceVersion string) error {
	if strings.HasPrefix(resourceVersion, "software-config|") {
		if operation != "software.configure" {
			return previewservice.ErrRequestChanged
		}
		parts := strings.Split(resourceVersion, "|")
		if len(parts) != 3 || !strings.HasPrefix(parts[1], "component=") || !strings.HasPrefix(parts[2], "revision=") {
			return previewservice.ErrRequestChanged
		}
		component := strings.TrimPrefix(parts[1], "component=")
		revision := strings.TrimPrefix(parts[2], "revision=")
		if component == "" || revision == "" {
			return previewservice.ErrRequestChanged
		}
		_, _, current, err := inspectSoftwareConfigurationTarget(ctx, component)
		if err != nil || !strings.EqualFold(current.Revision, revision) {
			return previewservice.ErrRequestChanged
		}
		return nil
	}
	if operation == "software.configure" {
		return previewservice.ErrRequestChanged
	}
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

func buildDocument(ctx context.Context, operation string, payload json.RawMessage) (previewservice.Document, string, error) {
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
		document.Actions = append([]previewservice.Action{{
			Type: "firewall", Name: "按需放行网站端口", DisplayCommand: "由受管防火墙适配器检查并按需放行网站端口",
		}}, document.Actions...)
		document.Impact.ModifyDatabase = true
		document.Impact.NetworkRisk = true
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
		var value input.InstallParams
		if err := json.Unmarshal(payload, &value); err != nil {
			return previewservice.Document{}, "", err
		}
		effectiveValues, err := softwareService.PreviewInstallationParams(ctx, &value)
		if err != nil {
			return previewservice.Document{}, "", err
		}
		document.EffectiveValues = make([]previewservice.EffectiveValue, 0, len(effectiveValues))
		for _, value := range effectiveValues {
			document.EffectiveValues = append(document.EffectiveValues, previewservice.EffectiveValue{
				Key:       value.Key,
				Value:     value.Value,
				Sensitive: value.Sensitive,
				Source:    value.Source,
			})
		}
		document.Prechecks = append(document.Prechecks, previewservice.Precheck{
			Name:    "安装参数",
			Status:  "passed",
			Message: "版本、端口及组件清单参数校验通过",
		})
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
		var value softwareConfigurationPayload
		if err := json.Unmarshal(payload, &value); err != nil {
			return previewservice.Document{}, "", err
		}
		changeSummary := "应用组件配置并保存历史"
		if value.RestoreFromID != "" {
			changeSummary = "恢复历史组件配置并保存历史"
		}
		document.Files = []previewservice.FileChange{{Path: "组件受管配置文件", Action: "update", ChangeSummary: changeSummary}}
		document.Actions = []previewservice.Action{{Type: "command", Name: "校验并应用组件配置", DisplayCommand: "由组件配置动作执行"}}
		document.EffectiveValues = effectiveConfigurationValues(value.Values)
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
		return document, softwareConfigurationTargetVersion(value.Component, value.Revision), nil
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

func effectiveConfigurationValues(values map[string]string) []previewservice.EffectiveValue {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]previewservice.EffectiveValue, 0, len(keys))
	for _, key := range keys {
		if strings.TrimSpace(values[key]) == "" {
			continue
		}
		result = append(result, previewservice.EffectiveValue{
			Key:    key,
			Value:  values[key],
			Source: "backend_normalized",
		})
	}
	return result
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
		if err := configsnapshot.Default().MarkWithAfter(snapshot.ID, result.WebServerConfigDocument, "succeeded", ""); err != nil {
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
		var value softwareConfigurationPayload
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		definition, _, current, err := inspectSoftwareConfigurationTarget(ctx, value.Component)
		if err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitConfiguration(definition.Component, value.Revision, current.Values, value.Values, value.RestoreFromID, userID)
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
	if err := snapshotService.MarkWithAfter(snapshot.ID, result.WebServerConfigDocument, "succeeded", ""); err != nil {
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
	var parameterErr *softwareService.InstallParameterError
	switch {
	case errors.As(err, &parameterErr):
		if userMessage := parameterErr.UserMessage(); userMessage != "" {
			core.HandleSimpleError(c, core.NewError(core.ErrInvalidParameter, userMessage))
			return
		}
		code, message = core.ErrInvalidParameter, "软件安装参数无效"
		detail = parameterErr.Error()
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
		detail = strings.TrimSpace(strings.TrimPrefix(err.Error(), fail2banservice.ErrValidation.Error()+":"))
		if detail == "" {
			detail = "请检查策略动作、模板和参数取值后重试。"
		}
	case errors.Is(err, fail2banservice.ErrRevisionConflict):
		code, message = core.ErrResourceStateInvalid, "规则已被其他操作修改，请刷新后重试"
	case errors.Is(err, fail2banservice.ErrProtectedAddress):
		code, message = core.ErrInsufficientPermissions, "该地址属于系统保护范围，不能封禁"
		detail = fail2banProtectedAddressDetail
	case errors.Is(err, fail2banservice.ErrUnavailable):
		code, message = core.ErrServiceUnavailable, "Fail2ban 未安装、未验证或服务不可用"
	}
	if detail == "" {
		core.HandleError(c, core.NewError(code, message))
		return
	}
	core.HandleError(c, core.NewErrorWithDetail(code, message, detail))
}
