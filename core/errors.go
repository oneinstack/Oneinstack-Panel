package core

import (
	"fmt"
	"net/http"
	"oneinstack/internal/i18n"
	"strings"

	"github.com/gin-gonic/gin"
)

// ErrorCode 错误码类型
type ErrorCode string

// 定义错误码常量
const (
	// 通用错误码
	ErrInternalError     ErrorCode = "INTERNAL_ERROR"
	ErrBadRequest        ErrorCode = "BAD_REQUEST"
	ErrUnauthorized      ErrorCode = "UNAUTHORIZED"
	ErrForbidden         ErrorCode = "FORBIDDEN"
	ErrNotFound          ErrorCode = "NOT_FOUND"
	ErrConflict          ErrorCode = "CONFLICT"
	ErrRateLimitExceeded ErrorCode = "RATE_LIMIT_EXCEEDED"

	// 认证相关错误码
	ErrInvalidToken    ErrorCode = "INVALID_TOKEN"
	ErrTokenExpired    ErrorCode = "TOKEN_EXPIRED"
	ErrInvalidPassword ErrorCode = "INVALID_PASSWORD"
	ErrWeakPassword    ErrorCode = "WEAK_PASSWORD"

	// 系统相关错误码
	ErrSystemError                 ErrorCode = "SYSTEM_ERROR"
	ErrCommandFailed               ErrorCode = "COMMAND_FAILED"
	ErrFileNotFound                ErrorCode = "FILE_NOT_FOUND"
	ErrPermissionDenied            ErrorCode = "PERMISSION_DENIED"
	ErrInsufficientStorage         ErrorCode = "INSUFFICIENT_STORAGE"
	ErrContainerRuntimeUnavailable ErrorCode = "CONTAINER_RUNTIME_UNAVAILABLE"

	// 业务相关错误码
	ErrUserNotFound     ErrorCode = "USER_NOT_FOUND"
	ErrUserExists       ErrorCode = "USER_EXISTS"
	ErrSoftwareNotFound ErrorCode = "SOFTWARE_NOT_FOUND"
	ErrWebsiteNotFound  ErrorCode = "WEBSITE_NOT_FOUND"
	ErrConfigError      ErrorCode = "CONFIG_ERROR"
)

// AppError 应用错误结构
type AppError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	MessageKey string    `json:"messageKey,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	Field      string    `json:"field,omitempty"`
}

// Error 实现error接口
func (e *AppError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("[%s] %s: %s", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
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
	Code    interface{}      `json:"code"`
	Message string           `json:"message"`
	Data    interface{}      `json:"data,omitempty"`
	Error   *AppError        `json:"error,omitempty"`
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
	message := detailedErrorMessage(err.Message, err.Detail)
	return &APIResponse{
		Success: false,
		Code:    err.Code,
		Message: message,
		Error: &AppError{
			Code:       err.Code,
			Message:    message,
			MessageKey: err.MessageKey,
			Detail:     explicitErrorDetail(err.Detail),
			Field:      err.Field,
		},
	}
}

func ErrorResponseForLocale(err *AppError, locale string) *APIResponse {
	response := ErrorResponse(err)
	if err.MessageKey != "" {
		response.Message = detailedErrorMessage(i18n.Message(locale, err.MessageKey, err.Message), err.Detail)
		response.Error.Message = response.Message
	}
	return response
}

// detailedErrorMessage keeps the stable high-level operation description while
// exposing the concrete cause in the top-level message for clients that do not
// inspect error.detail.
func detailedErrorMessage(message, detail string) string {
	message = strings.TrimSpace(message)
	detail = strings.TrimSpace(detail)
	if detail == "" || strings.Contains(message, detail) {
		return message
	}
	if message == "" {
		return detail
	}
	return fmt.Sprintf("%s：%s", message, detail)
}

func explicitErrorDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	if strings.HasPrefix(detail, "具体原因：") {
		return detail
	}
	return "具体原因：" + detail
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
	case ErrUnauthorized, ErrInvalidToken, ErrTokenExpired, ErrInvalidPassword:
		return http.StatusUnauthorized
	case ErrForbidden, ErrPermissionDenied:
		return http.StatusForbidden
	case ErrNotFound, ErrUserNotFound, ErrSoftwareNotFound, ErrWebsiteNotFound, ErrFileNotFound:
		return http.StatusNotFound
	case ErrConflict, ErrUserExists:
		return http.StatusConflict
	case ErrRateLimitExceeded:
		return http.StatusTooManyRequests
	case ErrInsufficientStorage:
		return http.StatusInsufficientStorage
	case ErrContainerRuntimeUnavailable:
		return http.StatusServiceUnavailable
	case ErrInternalError, ErrSystemError, ErrCommandFailed:
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
	Field   string `json:"field"`
	Message string `json:"message"`
	Value   string `json:"value,omitempty"`
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
	response := ErrorResponseForLocale(
		NewErrorWithDetail(ErrBadRequest, message, "字段校验失败："+errors.Error()),
		c.GetString("locale"),
	)
	response.Errors = errors
	c.JSON(http.StatusBadRequest, response)
}
