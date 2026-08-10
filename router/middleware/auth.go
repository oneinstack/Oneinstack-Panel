package middleware

import (
	"net/http"
	"net/url"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	containerservice "oneinstack/internal/services/container"
	securityservice "oneinstack/internal/services/security"
	sshservice "oneinstack/internal/services/ssh"
	"oneinstack/router/session"
	"oneinstack/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	ContextUsername           = "username"
	ContextUserID             = "userId"
	ContextTokenClaims        = "tokenClaims"
	ContextAuthMode           = "authMode"
	ContextRequestID          = "requestId"
	ContextSessionID          = "sessionId"
	ContextMustChangePassword = "mustChangePassword"

	AuthModeBearer = "bearer"
	AuthModeCookie = "cookie"
	AuthModeTicket = "ticket"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isContainerTerminalOpenPath(c.Request.URL.Path) && c.Query("ticket") != "" {
			claims, err := containerservice.DefaultTerminalTickets.Consume(c.Query("ticket"), c.ClientIP())
			if err != nil {
				writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidTerminalTicket, "容器终端票据无效或已过期", "容器终端票据校验失败，请重新获取票据。")
				return
			}
			if claims.UserAgent != c.GetHeader("User-Agent") || claims.ContainerReference != strings.TrimSpace(c.Param("id")) {
				writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidTerminalTicket, "容器终端票据无效", "当前请求与签发容器终端票据时绑定的客户端或容器不一致。")
				return
			}
			database := app.DB()
			if database == nil {
				writeAPIError(c, http.StatusServiceUnavailable, core.ErrSessionUnavailable, "会话服务不可用，请稍后重试", "服务端数据库未初始化，无法校验容器终端来源会话。")
				return
			}
			var account models.User
			if err := database.First(&account, claims.UserID).Error; err != nil ||
				account.Username != claims.Username || account.EffectiveSecurityVersion() != claims.SecurityVersion {
				writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "容器终端来源会话无效，请重新打开终端", "容器终端票据关联的用户或安全版本与当前服务端记录不一致。")
				return
			}
			if _, err := securityservice.NewSessionManager(database).Validate(claims.SourceSessionID, claims.UserID, claims.SecurityVersion); err != nil {
				writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "容器终端来源会话已过期或撤销，请重新打开终端", "请重新登录后再打开容器终端。")
				return
			}
			c.Set(ContextUsername, claims.Username)
			c.Set(ContextUserID, claims.UserID)
			c.Set(ContextTokenClaims, claims)
			c.Set(ContextAuthMode, AuthModeTicket)
			c.Set(ContextSessionID, claims.SourceSessionID)
			c.Set(ContextMustChangePassword, account.MustChangePassword)
			c.Next()
			return
		}
		if c.Request.URL.Path == "/v1/ssh/open" && c.Query("ticket") != "" {
			claims, err := sshservice.DefaultTickets.Consume(c.Query("ticket"), c.ClientIP())
			if err != nil {
				writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidTerminalTicket, "终端票据无效或已过期", "终端票据校验失败，请重新打开终端并获取新的票据。")
				return
			}
			if claims.UserAgent != c.GetHeader("User-Agent") {
				writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidTerminalTicket, "终端票据无效", "当前请求的 User-Agent 与签发终端票据时不一致，请重新打开终端。")
				return
			}
			database := app.DB()
			if database == nil {
				writeAPIError(c, http.StatusServiceUnavailable, core.ErrSessionUnavailable, "会话服务不可用，请稍后重试", "服务端数据库未初始化，无法校验终端来源会话。")
				return
			}
			var account models.User
			if err := database.First(&account, claims.UserID).Error; err != nil ||
				account.Username != claims.Username ||
				account.EffectiveSecurityVersion() != claims.SecurityVersion {
				writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "终端来源会话无效，请重新打开终端", "终端票据关联的用户或安全版本与当前服务端记录不一致。")
				return
			}
			sessionManager := securityservice.NewSessionManager(database)
			if _, err := sessionManager.Validate(
				claims.SourceSessionID,
				claims.UserID,
				claims.SecurityVersion,
			); err != nil {
				writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "终端来源会话已过期或撤销，请重新打开终端", "请重新登录后再打开终端。")
				return
			}
			c.Set(ContextUsername, claims.Username)
			c.Set(ContextUserID, claims.UserID)
			c.Set(ContextTokenClaims, claims)
			c.Set(ContextAuthMode, AuthModeTicket)
			c.Set(ContextSessionID, claims.SourceSessionID)
			c.Set(ContextMustChangePassword, account.MustChangePassword)
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		token := ""
		authMode := AuthModeBearer
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidTokenFormat, "Authorization 请求头格式无效，请使用 Bearer Token", "应使用 Bearer <token> 格式传递访问令牌。")
				return
			}
			token = parts[1]
			if len(token) == 0 {
				writeAPIError(c, http.StatusUnauthorized, core.ErrEmptyToken, "访问令牌不能为空", "Authorization 请求头未提供有效的 Bearer 令牌。")
				return
			}
		} else if cookie, err := c.Request.Cookie(session.CookieName); err == nil {
			token = cookie.Value
			authMode = AuthModeCookie
		}

		if token == "" {
			writeAPIError(c, http.StatusUnauthorized, core.ErrMissingToken, "请先登录后再访问此接口", "请在请求中提供有效的 Bearer 令牌或登录会话 Cookie。")
			return
		}

		claims, err := utils.ValidateJWT(token)
		if err != nil {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			writeAPIError(c, http.StatusUnauthorized, core.ErrInvalidToken, "访问令牌无效或已过期，请重新登录", "令牌校验失败："+err.Error())
			return
		}
		if claims.SessionID == "" {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			writeAPIError(c, http.StatusUnauthorized, core.ErrSessionRequired, "当前会话缺少安全标识，请退出后重新登录", "请退出后重新登录，以签发包含服务端会话标识的新令牌。")
			return
		}
		database := app.DB()
		if database == nil {
			writeAPIError(c, http.StatusServiceUnavailable, core.ErrSessionUnavailable, "会话服务不可用，请稍后重试", "服务端数据库未初始化，无法校验当前登录会话。")
			return
		}
		var account models.User
		if err := database.First(&account, claims.Id).Error; err != nil ||
			account.EffectiveSecurityVersion() != claims.SecurityVersion {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "登录会话已失效，请重新登录", "用户安全版本与会话记录不一致，可能是密码或安全配置已变更。")
			return
		}
		sessionManager := securityservice.NewSessionManager(database)
		if _, err := sessionManager.Validate(
			claims.SessionID, claims.Id, claims.SecurityVersion,
		); err != nil {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			writeAPIError(c, http.StatusUnauthorized, core.ErrSessionInvalidated, "登录会话已过期或已撤销，请重新登录", "请重新登录后再继续操作。")
			return
		}

		// 设置用户上下文信息
		c.Set(ContextUsername, account.Username)
		c.Set(ContextUserID, claims.Id)
		c.Set(ContextTokenClaims, claims)
		c.Set(ContextAuthMode, authMode)
		c.Set(ContextSessionID, claims.SessionID)
		c.Set(ContextMustChangePassword, account.MustChangePassword)
		if access, loadErr := accessservice.NewService(database).LoadUserAccess(claims.Id); loadErr == nil {
			c.Set(ContextUserAccess, access)
		}

		c.Next()
	}
}

func isContainerTerminalOpenPath(path string) bool {
	if !strings.HasPrefix(path, "/v1/containers/") || !strings.HasSuffix(path, "/terminal/open") {
		return false
	}
	rest := strings.TrimPrefix(path, "/v1/containers/")
	parts := strings.Split(rest, "/")
	return len(parts) == 3 && parts[0] != "" && parts[1] == "terminal" && parts[2] == "open"
}

// RequirePasswordChange blocks the management plane until an initial
// administrator replaces the bootstrap password. Logout, password replacement
// and read-only security/session status remain reachable.
func RequirePasswordChange() gin.HandlerFunc {
	return func(c *gin.Context) {
		required, _ := c.Get(ContextMustChangePassword)
		mustChange, _ := required.(bool)
		if !mustChange || firstLoginAllowed(c.Request.Method, c.Request.URL.Path) {
			c.Next()
			return
		}
		writeAPIError(c, http.StatusForbidden, core.ErrPasswordChangeRequired, "请先修改初始密码后再访问此接口", "当前账号仍使用初始密码，完成密码修改前不能访问该管理接口。")
	}
}

func firstLoginAllowed(method, path string) bool {
	if method == http.MethodPost {
		return path == "/v1/logout" || path == "/v1/sys/resetpassword"
	}
	if method == http.MethodGet {
		return path == "/v1/security/status" || path == "/v1/sessions"
	}
	return false
}

// CSRFMiddleware protects state-changing cookie-authenticated requests. Bearer
// clients remain supported because their credential is not attached by browsers.
func CSRFMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		mode, _ := c.Get(ContextAuthMode)
		if mode != AuthModeCookie || isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}

		origin, err := url.Parse(c.GetHeader("Origin"))
		if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") ||
			origin.User != nil || !strings.EqualFold(origin.Host, c.Request.Host) {
			writeAPIError(c, http.StatusForbidden, core.ErrCSRFRejected, "请求来源无效，请刷新页面后重试", "Cookie 会话请求的 Origin 与当前面板地址不匹配。")
			return
		}
		c.Next()
	}
}

func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func AuthenticatedUserID(c *gin.Context) (int64, bool) {
	value, exists := c.Get(ContextUserID)
	if !exists {
		return 0, false
	}
	id, ok := value.(int64)
	return id, ok && id > 0
}

func AuthenticatedSessionID(c *gin.Context) (string, bool) {
	value, exists := c.Get(ContextSessionID)
	if !exists {
		return "", false
	}
	id, ok := value.(string)
	return id, ok && id != ""
}
