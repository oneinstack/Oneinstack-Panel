package core

import (
	"fmt"
	"net/http"
	"oneinstack/internal/i18n"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
)

// Nginx/OpenResty may report the temporary candidate, the main file, or an
// included .conf file. Keep the basename generic so diagnostics do not fall
// back to the unhelpful generic message for site configurations.
var webServerConfigLinePattern = regexp.MustCompile(`(?i)([a-z0-9_.-]+\.conf):(\d+)(?::\s*)?(.*)$`)

// ErrorCode 错误码类型。
// 0 is reserved for successful responses; non-zero values are stable API codes.
type ErrorCode int

// 定义错误码常量
const (
	// 通用错误码
	ErrInternalError     ErrorCode = 1003
	ErrBadRequest        ErrorCode = 1000
	ErrUnauthorized      ErrorCode = 1100
	ErrForbidden         ErrorCode = 1200
	ErrNotFound          ErrorCode = 1001
	ErrConflict          ErrorCode = 1002
	ErrRateLimitExceeded ErrorCode = 1004
	ErrValidationFailed  ErrorCode = 1005
	ErrInvalidParameter  ErrorCode = 1006
	ErrRequiredField     ErrorCode = 1007
	ErrInvalidID         ErrorCode = 1008

	// 认证相关错误码
	ErrInvalidToken           ErrorCode = 1101
	ErrTokenExpired           ErrorCode = 1102
	ErrInvalidPassword        ErrorCode = 1103
	ErrWeakPassword           ErrorCode = 1104
	ErrMissingToken           ErrorCode = 1105
	ErrEmptyToken             ErrorCode = 1106
	ErrInvalidTokenFormat     ErrorCode = 1107
	ErrSessionRequired        ErrorCode = 1108
	ErrSessionInvalidated     ErrorCode = 1109
	ErrSessionUnavailable     ErrorCode = 1110
	ErrInvalidTerminalTicket  ErrorCode = 1111
	ErrPasswordChangeRequired ErrorCode = 1112

	// 系统相关错误码
	ErrPermissionDenied        ErrorCode = 1201
	ErrAdminRequired           ErrorCode = 1202
	ErrInsufficientPermissions ErrorCode = 1203
	ErrPermissionUnavailable   ErrorCode = 1204
	ErrCSRFRejected            ErrorCode = 1205
	ErrApprovalSelfReview      ErrorCode = 1206

	// 系统、配置及依赖错误码
	ErrSystemError                 ErrorCode = 3000
	ErrCommandFailed               ErrorCode = 3001
	ErrFileNotFound                ErrorCode = 2004
	ErrInsufficientStorage         ErrorCode = 3003
	ErrContainerRuntimeUnavailable ErrorCode = 3004
	ErrWebUIUnavailable            ErrorCode = 3005
	ErrServiceUnavailable          ErrorCode = 3006
	ErrConfigReadFailed            ErrorCode = 3007
	ErrConfigValidateFailed        ErrorCode = 3008
	ErrConfigApplyFailed           ErrorCode = 3009

	// 业务相关错误码
	ErrUserNotFound         ErrorCode = 2000
	ErrUserExists           ErrorCode = 2001
	ErrSoftwareNotFound     ErrorCode = 2002
	ErrWebsiteNotFound      ErrorCode = 2003
	ErrResourceStateInvalid ErrorCode = 2006
	ErrOperationExpired     ErrorCode = 2007
	ErrConfigError          ErrorCode = 3002

	// 任务及操作错误码
	ErrOperationFailed        ErrorCode = 4000
	ErrOperationNotConfirmed  ErrorCode = 4001
	ErrTaskCanceled           ErrorCode = 4002
	ErrTaskTimeout            ErrorCode = 4003
	ErrTaskServiceUnavailable ErrorCode = 4004
)

// AppError 应用错误结构
type AppError struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	MessageKey   string    `json:"messageKey,omitempty"`
	Detail       string    `json:"-"`
	Field        string    `json:"-"`
	PublicDetail bool      `json:"-"`
}

// Error 实现error接口
func (e *AppError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("[%d] %s: %s", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("[%d] %s", e.Code, e.Message)
}

// NewError 创建新的应用错误
func NewError(code ErrorCode, message string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
	}
}

func NewErrorKey(code ErrorCode, messageKey, fallback string) *AppError {
	err := NewError(code, fallback)
	err.MessageKey = messageKey
	return err
}

// NewErrorWithDetail 创建带详细信息的应用错误
func NewErrorWithDetail(code ErrorCode, message, detail string) *AppError {
	return &AppError{
		Code:         code,
		Message:      message,
		Detail:       detail,
		PublicDetail: true,
	}
}

// NewFieldError 创建字段验证错误
func NewFieldError(code ErrorCode, message, field string) *AppError {
	return &AppError{
		Code:    code,
		Message: message,
		Field:   field,
	}
}

// APIResponse 统一的API响应结构
type APIResponse struct {
	Success bool             `json:"success"`
	Code    ErrorCode        `json:"code"`
	Message string           `json:"message"`
	Data    interface{}      `json:"data"`
	Error   *APIError        `json:"error,omitempty"`
	Errors  ValidationErrors `json:"errors,omitempty"`
}

// APIError is the machine-readable error envelope. Detail is intended for
// diagnostics and is kept separate from the user-facing message.
type APIError struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Detail  string    `json:"detail,omitempty"`
	Field   string    `json:"field,omitempty"`
}

// SuccessResponse 成功响应
func SuccessResponse(data interface{}) *APIResponse {
	return SuccessResponseForLocale(i18n.LocaleZhCN, data)
}

func SuccessResponseForLocale(locale string, data interface{}) *APIResponse {
	return &APIResponse{
		Success: true,
		Code:    0,
		Message: i18n.Message(locale, i18n.MessageOperationSucceeded, "操作成功"),
		Data:    i18n.LocalizeResponseData(locale, data),
	}
}

// SuccessResponseForContext keeps non-200 responses such as 201/202 localized
// without forcing handlers to duplicate locale lookup logic.
func SuccessResponseForContext(c *gin.Context, data interface{}) *APIResponse {
	return SuccessResponseForLocale(c.GetString("locale"), data)
}

// ErrorResponse 错误响应
func ErrorResponse(err *AppError) *APIResponse {
	message := normalizeErrorMessage(err.Message)
	response := &APIResponse{
		Success: false,
		Code:    err.Code,
		Message: message,
		Data:    nil,
		Error: &APIError{
			Code:    err.Code,
			Message: message,
			Detail:  safeErrorDetail(err),
			Field:   err.Field,
		},
	}
	if err.Field != "" {
		response.Errors = ValidationErrors{{
			Field:   err.Field,
			Code:    err.Code,
			Message: message,
		}}
	}
	return response
}

func safeErrorDetail(err *AppError) string {
	detail := strings.TrimSpace(err.Detail)
	if detail == "" {
		return ""
	}

	if classified := classifyErrorDetail(detail); classified != "" {
		return classified
	}
	if err.PublicDetail && !containsSensitiveErrorDetail(detail) {
		return detail
	}
	// WrapError keeps the underlying error for server-side diagnostics, but
	// that text is often an implementation detail and cannot be returned as-is.
	// Preserve a safe, handler-provided message instead of replacing a specific
	// business reason with the generic detail for the error code.
	if message := safePublicErrorMessage(err); message != "" {
		return message
	}
	return defaultErrorDetail(err.Code)
}

func safePublicErrorMessage(err *AppError) string {
	message := normalizeErrorMessage(strings.TrimSpace(err.Message))
	if message == "" || message == strings.TrimSpace(err.Detail) || strings.ContainsAny(message, "\r\n") {
		return ""
	}
	if containsSensitiveErrorDetail(message) {
		return ""
	}
	return message
}

func classifyErrorDetail(detail string) string {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "web server configuration validation failed"),
		strings.Contains(lower, "configuration validation failed"):
		return formatWebServerConfigValidationDetail(detail)
	case strings.Contains(lower, "editable file exceeds"),
		strings.Contains(lower, "file content exceeds online editing limit"):
		return "文件大小超过在线编辑限制，请缩小文件后重试。"
	case strings.Contains(lower, "upload exceeds"),
		strings.Contains(lower, "uploaded file is too large"):
		return "上传文件超过允许大小，请选择较小的文件后重试。"
	case strings.Contains(lower, "image preview exceeds"):
		return "图片超过在线预览大小限制，请选择较小的图片后重试。"
	case strings.Contains(lower, "download size limit exceeded"):
		return "远程文件超过允许大小，请选择较小的文件后重试。"
	case strings.Contains(lower, "record not found"),
		strings.Contains(lower, "no longer available"):
		return "目标资源不存在、已被删除或状态已变化，请刷新列表并确认资源标识后重试。"
	case strings.Contains(lower, "no managed mysql account"),
		strings.Contains(lower, "managed root credential is unavailable"):
		return "当前数据库没有可用的面板托管账号，请重新同步数据库连接或重置托管账号凭据后重试。"
	case strings.Contains(lower, "decrypt"),
		strings.Contains(lower, "cipher"),
		strings.Contains(lower, "credential corrupt"):
		return "已保存的凭据无法读取，可能是加密配置已变更或记录已损坏，请重新设置该凭据。"
	case strings.Contains(lower, "deadline exceeded"),
		strings.Contains(lower, "timed out"),
		strings.Contains(lower, "timeout"):
		return "操作在限定时间内未完成，请检查目标服务状态和网络连通性后重试。"
	case strings.Contains(lower, "context canceled"),
		strings.Contains(lower, "context cancelled"):
		return "请求已取消，当前操作未完成；请确认页面或网络连接稳定后重试。"
	case strings.Contains(lower, "connection refused"):
		return "目标服务拒绝连接，请确认服务已启动、监听地址和端口配置正确。"
	case strings.Contains(lower, "no such host"),
		strings.Contains(lower, "server misbehaving"):
		return "目标主机解析失败，请检查主机地址和 DNS 配置后重试。"
	case strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "operation not permitted"):
		return "面板进程缺少完成该操作所需的系统权限，请检查运行用户和目标资源权限。"
	case strings.Contains(lower, "executable file not found"),
		strings.Contains(lower, "command not found"):
		return "服务器缺少执行该操作所需的命令，请安装对应依赖并确认命令可被面板进程访问。"
	case strings.Contains(lower, "no such file or directory"):
		return "操作依赖的文件或目录不存在，请确认相关组件已正确安装并完成配置。"
	case strings.Contains(lower, "database is not initialized"):
		return "面板数据库尚未初始化，请检查面板启动状态和数据库配置后重试。"
	case strings.Contains(lower, "database is locked"),
		strings.Contains(lower, "resource busy"):
		return "目标资源正被其他操作占用，请等待当前操作完成后重试。"
	case strings.Contains(lower, "already exists"),
		strings.Contains(lower, "duplicate"):
		return "相同名称或标识的资源已存在，请更换后重试，或刷新列表后操作现有资源。"
	case strings.Contains(lower, "cannot unmarshal"),
		strings.Contains(lower, "invalid character") && (strings.Contains(lower, "json") || strings.Contains(lower, "looking for")):
		return "请求 JSON 中存在类型或格式不正确的字段，请按接口定义检查提交内容。"
	case strings.Contains(lower, "failed on the 'required' tag"):
		return "请求中缺少必填字段，请按接口定义补充完整后重试。"
	case strings.Contains(lower, "failed on the '") && strings.Contains(lower, "tag"):
		return "请求字段未通过格式或取值范围校验，请按接口定义修正后重试。"
	case strings.Contains(lower, "request body too large"):
		return "请求内容超过接口允许的大小，请缩减提交内容后重试。"
	case lower == "eof" || strings.Contains(lower, "unexpected end of json input"):
		return "请求体为空或 JSON 内容不完整，请提交完整的请求数据。"
	default:
		return ""
	}
}

func formatWebServerConfigValidationDetail(detail string) string {
	restored := strings.Contains(strings.ToLower(detail), "previous content restored") ||
		strings.Contains(strings.ToLower(detail), "original configuration restored")
	suffix := "预览阶段未写入原配置，请修正后重新预览。"
	if restored {
		suffix = "原配置已自动恢复，请修正后重新预览。"
	}
	for _, line := range strings.Split(strings.ReplaceAll(detail, "\r\n", "\n"), "\n") {
		matches := webServerConfigLinePattern.FindStringSubmatch(line)
		if len(matches) != 4 {
			continue
		}
		diagnostic := strings.TrimSpace(matches[3])
		prefix := strings.TrimSpace(line[:strings.Index(line, matches[0])])
		// Nginx commonly prints: nginx: [emerg] <reason> in <path>:<line>.
		// Keep the reason but omit the disposable/absolute path.
		if marker := strings.LastIndex(prefix, "] "); marker >= 0 {
			prefix = strings.TrimSpace(prefix[marker+2:])
		}
		if marker := strings.LastIndex(prefix, " in "); marker >= 0 {
			prefix = strings.TrimSpace(prefix[:marker])
		}
		if prefix != "" {
			diagnostic = prefix
		}
		if diagnostic == "" {
			return fmt.Sprintf("Web Server 配置语法错误：第 %s 行。%s", matches[2], suffix)
		}
		return fmt.Sprintf("Web Server 配置语法错误：第 %s 行；Nginx 诊断：%s。%s", matches[2], diagnostic, suffix)
	}
	if restored {
		return "Web Server 配置语法校验失败，原配置已自动恢复；请检查 Nginx/OpenResty 指令格式后重新预览。"
	}
	if preflight := formatWebServerConfigPreflightDetail(detail); preflight != "" {
		return fmt.Sprintf("Web Server 配置预检失败：%s。%s", preflight, suffix)
	}
	return "Web Server 配置语法校验失败，预览阶段未写入原配置；请检查 Nginx/OpenResty 指令格式后重新预览。"
}

// formatWebServerConfigPreflightDetail keeps errors raised while assembling
// the disposable preview configuration actionable. These errors happen
// before nginx -t, so they do not contain a line number to parse.
func formatWebServerConfigPreflightDetail(detail string) string {
	lower := strings.ToLower(detail)
	switch {
	case strings.Contains(lower, "configuration exceeds the"):
		return "配置内容超过允许大小限制"
	case strings.Contains(lower, "configuration contains a nul byte"):
		return "配置内容包含不允许的 NUL 字节"
	case strings.Contains(lower, "include path is empty"):
		return "include 指令未提供文件路径"
	case strings.Contains(lower, "dynamic web server configuration include"):
		return "不支持包含变量的动态 include，请改用固定文件路径"
	case strings.Contains(lower, "include nesting exceeds"):
		return "include 嵌套层级超过允许限制"
	case strings.Contains(lower, "includes exceed"):
		return "include 依赖文件数量超过允许限制"
	case strings.Contains(lower, "include is missing"):
		return "include 依赖文件不存在"
	case strings.Contains(lower, "include is outside the managed roots"):
		return "include 依赖文件超出受管配置目录范围"
	case strings.Contains(lower, "include escapes the managed root"):
		return "include 路径逃逸受管配置目录"
	case strings.Contains(lower, "include cannot be a symbolic link"):
		return "include 依赖文件不能是符号链接"
	case strings.Contains(lower, "invalid web server configuration include"):
		return "include 文件路径格式无效"
	case strings.Contains(lower, "read web server include"):
		return "include 依赖文件无法读取"
	case strings.Contains(lower, "stage web server include"),
		strings.Contains(lower, "create web server include directory"):
		return "include 依赖文件无法暂存到预览目录"
	default:
		return ""
	}
}

func containsSensitiveErrorDetail(detail string) bool {
	lower := strings.ToLower(detail)
	sensitiveFragments := []string{
		"password", "passwd", "secret", "private key", "authorization", "bearer ",
		"cookie", "access_token", "refresh_token", "token=", "\"token\"", "enc:v1",
		"select ", "insert ", "update ", "delete from ",
		"sqlite", "gorm", "/users/", "/home/", "/root/", "/etc/", "/var/", "/tmp/",
		"c:\\", "d:\\",
	}
	for _, fragment := range sensitiveFragments {
		if strings.Contains(lower, fragment) {
			return true
		}
	}
	return false
}

func defaultErrorDetail(code ErrorCode) string {
	switch code {
	case ErrBadRequest, ErrValidationFailed, ErrInvalidParameter, ErrRequiredField,
		ErrInvalidID, ErrWeakPassword, ErrConfigValidateFailed, ErrConfigError:
		return "请检查请求字段是否完整，并确认字段类型、格式和取值范围符合接口要求。"
	case ErrUnauthorized, ErrInvalidToken, ErrTokenExpired, ErrInvalidPassword,
		ErrMissingToken, ErrEmptyToken, ErrInvalidTokenFormat, ErrSessionRequired,
		ErrSessionInvalidated, ErrInvalidTerminalTicket:
		return "当前登录凭据无效或已过期，请重新登录后再执行该操作。"
	case ErrApprovalSelfReview:
		return "审批申请人不能审批或拒绝自己的申请，请由其他有审批权限的账号处理。"
	case ErrForbidden, ErrPermissionDenied, ErrAdminRequired, ErrInsufficientPermissions,
		ErrCSRFRejected, ErrPasswordChangeRequired:
		return "当前账号缺少执行该操作所需的权限或安全条件，请联系管理员授权后重试。"
	case ErrNotFound, ErrUserNotFound, ErrSoftwareNotFound, ErrWebsiteNotFound, ErrFileNotFound:
		return "目标资源不存在或已被删除，请刷新列表并确认资源标识后重试。"
	case ErrConflict, ErrUserExists, ErrResourceStateInvalid, ErrOperationExpired,
		ErrOperationNotConfirmed, ErrTaskCanceled:
		return "资源当前状态不允许该操作，请刷新最新状态并按提示完成前置步骤后重试。"
	case ErrRateLimitExceeded:
		return "请求过于频繁，请等待一段时间后重试。"
	case ErrInsufficientStorage:
		return "服务器可用存储空间不足，请释放空间或调整存储位置后重试。"
	case ErrContainerRuntimeUnavailable, ErrSessionUnavailable, ErrPermissionUnavailable,
		ErrWebUIUnavailable, ErrServiceUnavailable, ErrTaskServiceUnavailable:
		return "依赖服务尚未启动或初始化失败，请检查服务状态和面板配置后重试。"
	case ErrTaskTimeout:
		return "任务在限定时间内未完成，请检查目标服务状态和网络连通性后重试。"
	case ErrCommandFailed:
		return "受控系统命令执行失败，请检查相关组件状态、运行权限和服务器日志。"
	case ErrConfigReadFailed:
		return "配置或凭据无法读取，请检查配置是否存在、权限是否正确，必要时重新保存配置。"
	case ErrConfigApplyFailed:
		return "配置应用失败，请检查配置内容和目标服务状态，修正后重新提交。"
	case ErrInternalError, ErrSystemError, ErrOperationFailed:
		return "服务器处理当前操作时发生异常，请稍后重试；若持续失败，请结合请求时间查看服务器日志。"
	default:
		return "当前操作未完成，请刷新状态后重试；若持续失败，请查看服务器日志。"
	}
}

func ErrorResponseForLocale(err *AppError, locale string) *APIResponse {
	response := ErrorResponse(err)
	if err.MessageKey != "" {
		response.Message = normalizeErrorMessage(i18n.Message(locale, err.MessageKey, err.Message))
	} else {
		response.Message = localizedErrorMessage(locale, response.Message, err.Code)
	}
	if response.Error != nil {
		response.Error.Message = response.Message
		response.Error.Detail = localizedErrorDetail(locale, response.Error.Detail, err.Code)
	}
	for index := range response.Errors {
		response.Errors[index].Message = localizedErrorMessage(locale, response.Errors[index].Message, response.Errors[index].Code)
	}
	return response
}

func localizedErrorMessage(locale, message string, code ErrorCode) string {
	translated := i18n.LocalizeText(locale, message)
	if i18n.Canonical(locale) != i18n.LocaleEnUS || !i18n.ContainsHan(translated) {
		return translated
	}
	return defaultEnglishErrorMessage(code)
}

func localizedErrorDetail(locale, detail string, code ErrorCode) string {
	translated := i18n.LocalizeText(locale, detail)
	if i18n.Canonical(locale) != i18n.LocaleEnUS || !i18n.ContainsHan(translated) {
		return translated
	}
	return defaultEnglishErrorDetail(code)
}

func defaultEnglishErrorMessage(code ErrorCode) string {
	switch code {
	case ErrBadRequest, ErrValidationFailed, ErrInvalidParameter, ErrRequiredField,
		ErrInvalidID, ErrWeakPassword, ErrConfigValidateFailed, ErrConfigError:
		return "The request parameters are invalid"
	case ErrUnauthorized, ErrInvalidToken, ErrTokenExpired, ErrInvalidPassword,
		ErrMissingToken, ErrEmptyToken, ErrInvalidTokenFormat, ErrSessionRequired,
		ErrSessionInvalidated, ErrInvalidTerminalTicket:
		return "Authentication is required or has expired"
	case ErrApprovalSelfReview:
		return "The requester cannot review their own approval request"
	case ErrForbidden, ErrPermissionDenied, ErrAdminRequired, ErrInsufficientPermissions,
		ErrCSRFRejected, ErrPasswordChangeRequired:
		return "You do not have permission to perform this operation"
	case ErrNotFound, ErrUserNotFound, ErrSoftwareNotFound, ErrWebsiteNotFound, ErrFileNotFound:
		return "The requested resource was not found"
	case ErrConflict, ErrUserExists, ErrResourceStateInvalid, ErrOperationExpired,
		ErrOperationNotConfirmed, ErrTaskCanceled:
		return "The resource state conflicts with this operation"
	case ErrRateLimitExceeded:
		return "Too many requests"
	case ErrInsufficientStorage:
		return "Insufficient storage space"
	case ErrContainerRuntimeUnavailable, ErrSessionUnavailable, ErrPermissionUnavailable,
		ErrWebUIUnavailable, ErrServiceUnavailable, ErrTaskServiceUnavailable:
		return "A required service is unavailable"
	case ErrTaskTimeout:
		return "The operation timed out"
	case ErrConfigReadFailed:
		return "Failed to read the configuration or credential"
	case ErrConfigApplyFailed:
		return "Failed to apply the configuration"
	default:
		return "The server could not complete the operation"
	}
}

func defaultEnglishErrorDetail(code ErrorCode) string {
	switch code {
	case ErrBadRequest, ErrValidationFailed, ErrInvalidParameter, ErrRequiredField,
		ErrInvalidID, ErrWeakPassword, ErrConfigValidateFailed, ErrConfigError:
		return "Check that all required fields are present and that their types, formats, and value ranges match the API contract."
	case ErrUnauthorized, ErrInvalidToken, ErrTokenExpired, ErrInvalidPassword,
		ErrMissingToken, ErrEmptyToken, ErrInvalidTokenFormat, ErrSessionRequired,
		ErrSessionInvalidated, ErrInvalidTerminalTicket:
		return "The current login credential is missing, invalid, or expired. Sign in again and retry the operation."
	case ErrApprovalSelfReview:
		return "The requester cannot approve or reject their own request. Ask another account with approval permission to review it."
	case ErrForbidden, ErrPermissionDenied, ErrAdminRequired, ErrInsufficientPermissions,
		ErrCSRFRejected, ErrPasswordChangeRequired:
		return "The current account does not meet the permission or security requirements for this operation. Contact an administrator if access is required."
	case ErrNotFound, ErrUserNotFound, ErrSoftwareNotFound, ErrWebsiteNotFound, ErrFileNotFound:
		return "The target resource does not exist or has been removed. Refresh the list and verify the resource identifier before retrying."
	case ErrConflict, ErrUserExists, ErrResourceStateInvalid, ErrOperationExpired,
		ErrOperationNotConfirmed, ErrTaskCanceled:
		return "The resource is not in a state that allows this operation. Refresh its status and complete any required prerequisite before retrying."
	case ErrRateLimitExceeded:
		return "Requests are being sent too frequently. Wait before retrying."
	case ErrInsufficientStorage:
		return "The server does not have enough available storage. Free space or select another storage location before retrying."
	case ErrContainerRuntimeUnavailable, ErrSessionUnavailable, ErrPermissionUnavailable,
		ErrWebUIUnavailable, ErrServiceUnavailable, ErrTaskServiceUnavailable:
		return "A required service is not running or failed to initialize. Check the service status and Panel configuration before retrying."
	case ErrTaskTimeout:
		return "The operation did not finish within the allowed time. Check the target service and network connectivity before retrying."
	case ErrCommandFailed:
		return "A controlled system command failed. Check the component status, process permissions, and server logs."
	case ErrConfigReadFailed:
		return "The configuration or credential could not be read. Verify that it exists and has the correct permissions, or save it again."
	case ErrConfigApplyFailed:
		return "The configuration could not be applied. Check its contents and the target service status, then retry."
	default:
		return "The server encountered an error while processing this operation. Retry later and check the server logs using the request time if the problem continues."
	}
}

// normalizeErrorMessage keeps legacy handler messages useful to API clients
// while individual handlers are migrated to business-specific descriptions.
func normalizeErrorMessage(message string) string {
	switch message {
	case "请求参数错误", "请求参数无效":
		return "请求参数无效，请检查提交内容"
	case "请求参数格式错误":
		return "请求参数格式不正确，请检查字段类型和格式"
	case "操作失败", "操作异常", "请求失败":
		return "操作执行失败，请稍后重试"
	case "系统错误":
		return "系统处理异常，请稍后重试"
	default:
		return message
	}
}

// HandleSuccess 处理成功响应
func HandleSuccess(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, SuccessResponseForLocale(c.GetString("locale"), data))
}

// HandleError 处理错误响应
func HandleError(c *gin.Context, err *AppError) {
	statusCode := getHTTPStatusCode(err.Code)
	c.JSON(statusCode, ErrorResponseForLocale(err, c.GetString("locale")))
}

// HandleErrorWithStatus 处理带状态码的错误响应
func HandleErrorWithStatus(c *gin.Context, statusCode int, err *AppError) {
	c.JSON(statusCode, ErrorResponseForLocale(err, c.GetString("locale")))
}

// getHTTPStatusCode 根据错误码获取HTTP状态码
func getHTTPStatusCode(code ErrorCode) int {
	switch code {
	case ErrBadRequest, ErrWeakPassword, ErrConfigError:
		return http.StatusBadRequest
	case ErrValidationFailed, ErrInvalidParameter, ErrRequiredField, ErrInvalidID, ErrConfigValidateFailed:
		return http.StatusBadRequest
	case ErrUnauthorized, ErrInvalidToken, ErrTokenExpired, ErrInvalidPassword,
		ErrMissingToken, ErrEmptyToken, ErrInvalidTokenFormat, ErrSessionRequired,
		ErrSessionInvalidated, ErrInvalidTerminalTicket:
		return http.StatusUnauthorized
	case ErrForbidden, ErrPermissionDenied, ErrAdminRequired, ErrInsufficientPermissions,
		ErrCSRFRejected, ErrPasswordChangeRequired, ErrApprovalSelfReview:
		return http.StatusForbidden
	case ErrNotFound, ErrUserNotFound, ErrSoftwareNotFound, ErrWebsiteNotFound, ErrFileNotFound:
		return http.StatusNotFound
	case ErrConflict, ErrUserExists, ErrResourceStateInvalid, ErrOperationExpired,
		ErrOperationNotConfirmed, ErrTaskCanceled:
		return http.StatusConflict
	case ErrRateLimitExceeded:
		return http.StatusTooManyRequests
	case ErrInsufficientStorage:
		return http.StatusInsufficientStorage
	case ErrContainerRuntimeUnavailable, ErrSessionUnavailable, ErrPermissionUnavailable,
		ErrWebUIUnavailable, ErrServiceUnavailable, ErrTaskServiceUnavailable:
		return http.StatusServiceUnavailable
	case ErrTaskTimeout:
		return http.StatusGatewayTimeout
	case ErrInternalError, ErrSystemError, ErrCommandFailed, ErrConfigReadFailed,
		ErrConfigApplyFailed, ErrOperationFailed:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// WrapError 包装标准错误为应用错误
func WrapError(err error, code ErrorCode, message string) *AppError {
	if err == nil {
		return nil
	}

	return &AppError{
		Code:    code,
		Message: message,
		Detail:  err.Error(),
	}
}

// ValidationError 验证错误
type ValidationError struct {
	Field   string    `json:"field"`
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	Value   string    `json:"value,omitempty"`
}

// ValidationErrors 多个验证错误
type ValidationErrors []ValidationError

// Error 实现error接口
func (ve ValidationErrors) Error() string {
	if len(ve) == 0 {
		return "validation failed"
	}
	return fmt.Sprintf("validation failed: %s", ve[0].Message)
}

// HandleValidationErrors 处理验证错误
func HandleValidationErrors(c *gin.Context, errors ValidationErrors) {
	message := i18n.Message(c.GetString("locale"), i18n.MessageValidationFailed, "输入验证失败")
	response := ErrorResponseForLocale(NewError(ErrValidationFailed, message), c.GetString("locale"))
	response.Errors = errors
	for index := range response.Errors {
		response.Errors[index].Message = localizedErrorMessage(c.GetString("locale"), response.Errors[index].Message, response.Errors[index].Code)
	}
	c.JSON(http.StatusBadRequest, response)
}
