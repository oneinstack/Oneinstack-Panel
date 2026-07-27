package middleware

import (
	"net/http"
	"net/url"
	"oneinstack/app"
	"oneinstack/internal/models"
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
		if c.Request.URL.Path == "/v1/ssh/open" && c.Query("ticket") != "" {
			claims, err := sshservice.DefaultTickets.Consume(c.Query("ticket"), c.ClientIP())
			if err != nil {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "Invalid or expired terminal ticket",
					"code":  "INVALID_TERMINAL_TICKET",
				})
				c.Abort()
				return
			}
			c.Set(ContextUsername, claims.Username)
			c.Set(ContextUserID, claims.UserID)
			c.Set(ContextTokenClaims, claims)
			c.Set(ContextAuthMode, AuthModeTicket)
			c.Next()
			return
		}

		authHeader := c.GetHeader("Authorization")
		token := ""
		authMode := AuthModeBearer
		if authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) != 2 || parts[0] != "Bearer" {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "Invalid authorization header format. Expected: Bearer <token>",
					"code":  "INVALID_TOKEN_FORMAT",
				})
				c.Abort()
				return
			}
			token = parts[1]
			if len(token) == 0 {
				c.JSON(http.StatusUnauthorized, gin.H{
					"error": "Token cannot be empty",
					"code":  "EMPTY_TOKEN",
				})
				c.Abort()
				return
			}
		} else if cookie, err := c.Request.Cookie(session.CookieName); err == nil {
			token = cookie.Value
			authMode = AuthModeCookie
		}

		if token == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication is required",
				"code":  "MISSING_TOKEN",
			})
			c.Abort()
			return
		}

		claims, err := utils.ValidateJWT(token)
		if err != nil {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":  "Invalid or expired token",
				"code":   "INVALID_TOKEN",
				"detail": err.Error(),
			})
			c.Abort()
			return
		}
		if claims.SessionID == "" {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "This session predates server-side session security",
				"code":  "SESSION_REQUIRED",
			})
			c.Abort()
			return
		}
		database := app.DB()
		if database == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Session service is unavailable",
				"code":  "SESSION_UNAVAILABLE",
			})
			c.Abort()
			return
		}
		var account models.User
		if err := database.First(&account, claims.Id).Error; err != nil ||
			account.EffectiveSecurityVersion() != claims.SecurityVersion {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Session is no longer valid",
				"code":  "SESSION_INVALIDATED",
			})
			c.Abort()
			return
		}
		sessionManager := securityservice.NewSessionManager(database)
		if _, err := sessionManager.Validate(
			claims.SessionID, claims.Id, claims.SecurityVersion,
		); err != nil {
			if authMode == AuthModeCookie {
				session.Clear(c)
			}
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Session is expired or revoked",
				"code":  "SESSION_INVALIDATED",
			})
			c.Abort()
			return
		}

		// 设置用户上下文信息
		c.Set(ContextUsername, account.Username)
		c.Set(ContextUserID, claims.Id)
		c.Set(ContextTokenClaims, claims)
		c.Set(ContextAuthMode, authMode)
		c.Set(ContextSessionID, claims.SessionID)
		c.Set(ContextMustChangePassword, account.MustChangePassword)

		c.Next()
	}
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
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Password must be changed before using the panel",
			"code":  "PASSWORD_CHANGE_REQUIRED",
		})
		c.Abort()
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
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Cross-origin request rejected",
				"code":  "CSRF_REJECTED",
			})
			c.Abort()
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
