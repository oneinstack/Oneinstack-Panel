package security

import (
	"errors"
	"net/http"
	"strings"

	"oneinstack/app"
	"oneinstack/core"
	securityservice "oneinstack/internal/services/security"
	"oneinstack/router/middleware"
	"oneinstack/router/session"

	"github.com/gin-gonic/gin"
)

type verificationRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

func Status(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	status, err := securityservice.NewTOTPManager(app.DB()).Status(userID)
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	core.HandleSuccess(c, status)
}

func SetupTOTP(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	username, _ := c.Get(middleware.ContextUsername)
	setup, err := securityservice.NewTOTPManager(app.DB()).
		Setup(userID, strings.TrimSpace(valueString(username)))
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	core.HandleSuccess(c, setup)
}

func ConfirmTOTP(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	var request verificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请求参数格式错误"))
		return
	}
	codes, err := securityservice.NewTOTPManager(app.DB()).
		Confirm(userID, request.Password, request.Code)
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	currentID, _ := middleware.AuthenticatedSessionID(c)
	revoked, err := securityservice.NewSessionManager(app.DB()).
		RevokeOthers(userID, currentID, "totp_enabled")
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{
		"enabled": true, "recoveryCodes": codes, "revokedSessions": revoked,
	})
}

func DisableTOTP(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	var request verificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请求参数格式错误"))
		return
	}
	if err := securityservice.NewTOTPManager(app.DB()).
		Disable(userID, request.Password, request.Code); err != nil {
		handleSecurityError(c, err)
		return
	}
	session.Clear(c)
	core.HandleSuccess(c, gin.H{
		"enabled": false, "authenticated": false, "reauthenticationRequired": true,
	})
}

func RegenerateRecoveryCodes(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	var request verificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请求参数格式错误"))
		return
	}
	codes, err := securityservice.NewTOTPManager(app.DB()).
		RegenerateRecoveryCodes(userID, request.Password, request.Code)
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	currentID, _ := middleware.AuthenticatedSessionID(c)
	revoked, err := securityservice.NewSessionManager(app.DB()).
		RevokeOthers(userID, currentID, "recovery_codes_regenerated")
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"recoveryCodes": codes, "revokedSessions": revoked})
}

func ListSessions(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	currentID, _ := middleware.AuthenticatedSessionID(c)
	records, err := securityservice.NewSessionManager(app.DB()).ListActive(userID)
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	items := make([]gin.H, 0, len(records))
	for _, record := range records {
		items = append(items, gin.H{
			"id": record.ID, "username": record.Username,
			"remoteIp": record.RemoteIP, "userAgent": record.UserAgent,
			"createdAt": record.CreatedAt, "lastSeenAt": record.LastSeenAt,
			"expiresAt": record.ExpiresAt, "current": record.ID == currentID,
		})
	}
	core.HandleSuccess(c, items)
}

func RevokeSession(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(c.Param("id"))
	if sessionID == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "会话标识不能为空"))
		return
	}
	revoked, err := securityservice.NewSessionManager(app.DB()).
		Revoke(userID, sessionID, "user_revoked")
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	if !revoked {
		core.HandleErrorWithStatus(c, http.StatusNotFound,
			core.NewError(core.ErrNotFound, "会话不存在或已经失效"))
		return
	}
	currentID, _ := middleware.AuthenticatedSessionID(c)
	current := sessionID == currentID
	if current {
		session.Clear(c)
	}
	core.HandleSuccess(c, gin.H{"revoked": true, "current": current})
}

func RevokeOtherSessions(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	currentID, _ := middleware.AuthenticatedSessionID(c)
	count, err := securityservice.NewSessionManager(app.DB()).
		RevokeOthers(userID, currentID, "user_revoked_others")
	if err != nil {
		handleSecurityError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"revokedSessions": count})
}

func authenticatedUser(c *gin.Context) (int64, bool) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "登录状态无效"))
	}
	return userID, ok
}

func valueString(value interface{}) string {
	text, _ := value.(string)
	return text
}

func handleSecurityError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, securityservice.ErrInvalidPassword):
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前密码错误"))
	case errors.Is(err, securityservice.ErrInvalidSecondFactor):
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "动态口令或恢复码无效"))
	case errors.Is(err, securityservice.ErrMFAAlreadyEnabled):
		core.HandleErrorWithStatus(c, http.StatusConflict,
			core.NewError(core.ErrBadRequest, "动态口令认证已经启用"))
	case errors.Is(err, securityservice.ErrMFANotEnabled),
		errors.Is(err, securityservice.ErrMFAUnavailable):
		core.HandleError(c, core.NewError(core.ErrBadRequest, "动态口令认证尚未配置"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "安全设置操作失败"))
	}
}
