package core

import (
	"fmt"
	"net/http"
	"oneinstack/internal/i18n"

	"github.com/gin-gonic/gin"
)

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
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	MessageKey string    `json:"messageKey,omitempty"`
	Detail     string    `json:"-"`
	Field      string    `json:"-"`
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
		Code:    code,
		Message: message,
		Detail:  detail,
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
	Errors  ValidationErrors `json:"errors,omitempty"`
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
		Data:    data,
	}
}

// ErrorResponse 错误响应
func ErrorResponse(err *AppError) *APIResponse {
	message := normalizeErrorMessage(err.Message)
	response := &APIResponse{
		Success: false,
		Code:    err.Code,
		Message: message,
		Data:    nil,
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

func ErrorResponseForLocale(err *AppError, locale string) *APIResponse {
	response := ErrorResponse(err)
	if err.MessageKey != "" {
		response.Message = normalizeErrorMessage(i18n.Message(locale, err.MessageKey, err.Message))
		if len(response.Errors) > 0 {
			response.Errors[0].Message = response.Message
		}
	}
	return response
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
		ErrCSRFRejected, ErrPasswordChangeRequired:
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

	return NewErrorWithDetail(code, message, err.Error())
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
	c.JSON(http.StatusBadRequest, response)
}
