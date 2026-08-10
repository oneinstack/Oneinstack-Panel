package security

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/core"
	auditservice "oneinstack/internal/services/audit"
	securityservice "oneinstack/internal/services/security"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/middleware"
	"oneinstack/router/session"

	"github.com/gin-gonic/gin"
)

type verificationRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type passwordVerificationRequest struct {
	Password string `json:"password" binding:"required"`
}

// VerifyPassword verifies the currently authenticated user's panel password
// without changing the password or creating a new session.
func VerifyPassword(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	username := valueString(usernameValue(c))
	remoteIP := auditservice.RemoteIP(c.Request)
	if allowed, remaining := securityservice.PasswordVerificationAllowed(userID, remoteIP); !allowed {
		seconds := int64(remaining.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		c.Header("Retry-After", strconv.FormatInt(seconds, 10))
		auditservice.RecordAuthEvent(c, "auth.password_verify_blocked", username, userID,
			http.StatusTooManyRequests, "failure", "", "密码校验失败次数过多，正在冷却")
		core.HandleError(c, core.NewError(core.ErrRateLimitExceeded, "密码校验失败次数过多，请稍后再试"))
		return
	}
	var request passwordVerificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入当前面板密码"))
		return
	}

	account, verified := userservice.CheckUserPassword(username, request.Password)
	if !verified || account.ID != userID {
		locked, cooldown := securityservice.RecordPasswordVerificationFailure(userID, remoteIP)
		auditservice.RecordAuthEvent(c, "auth.password_verify", username, userID,
			http.StatusUnauthorized, "failure", "", "当前面板密码错误")
		if locked {
			c.Header("Retry-After", strconv.FormatInt(int64(cooldown.Seconds()), 10))
		}
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "当前面板密码错误"))
		return
	}

	securityservice.ResetPasswordVerificationFailures(userID, remoteIP)
	auditservice.RecordAuthEvent(c, "auth.password_verify", username, userID,
		http.StatusOK, "success", "", "")
	core.HandleSuccess(c, gin.H{"verified": true})
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "启用动态口令的验证参数格式不正确"))
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
	auditservice.RecordAuthEvent(c, "auth.totp_enabled", valueString(usernameValue(c)), userID, 200, "success", "", "")
}

func DisableTOTP(c *gin.Context) {
	userID, ok := authenticatedUser(c)
	if !ok {
		return
	}
	var request verificationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "停用动态口令的验证参数格式不正确"))
		return
	}
	if err := securityservice.NewTOTPManager(app.DB()).
		Disable(userID, request.Password, request.Code); err != nil {
		handleSecurityError(c, err)
		return
	}
	auditservice.RecordAuthEvent(c, "auth.totp_disabled", valueString(usernameValue(c)), userID, 200, "success", "", "")
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "重新生成恢复码的验证参数格式不正确"))
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
	auditservice.RecordAuthEvent(c, "auth.recovery_codes_regenerated", valueString(usernameValue(c)), userID, 200, "success", "", "")
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
	auditservice.RecordAuthEvent(c, "auth.session_revoke", valueString(usernameValue(c)), userID, 200, "success", "", "")
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
	auditservice.RecordAuthEvent(c, "auth.session_revoke", valueString(usernameValue(c)), userID, 200, "success", "", "")
	core.HandleSuccess(c, gin.H{"revokedSessions": count})
}

func usernameValue(c *gin.Context) interface{} {
	value, _ := c.Get(middleware.ContextUsername)
	return value
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
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, securityOperationMessage(c)))
	}
}

func securityOperationMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/security/status":
		return "读取账号安全设置失败"
	case "/v1/security/totp/setup":
		return "创建动态口令设置失败"
	case "/v1/security/totp/confirm":
		return "启用动态口令认证失败"
	case "/v1/security/totp/disable":
		return "停用动态口令认证失败"
	case "/v1/security/totp/recovery-codes/regenerate":
		return "重新生成动态口令恢复码失败"
	case "/v1/sessions":
		return "读取登录会话列表失败"
	case "/v1/sessions/revoke-others":
		return "撤销其他登录会话失败"
	case "/v1/sessions/:id/revoke":
		return "撤销指定登录会话失败"
	default:
		return "处理账号安全请求失败"
	}
}
