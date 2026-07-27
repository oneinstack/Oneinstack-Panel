package middleware

import (
	"log"
	"net/http"
	"oneinstack/app"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/utils"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Permission 权限定义
type Permission string

const (
	PermissionSystemRead    Permission = "system:read"
	PermissionSystemWrite   Permission = "system:write"
	PermissionUserManage    Permission = "user:manage"
	PermissionSoftwareRead  Permission = "software:read"
	PermissionSoftwareWrite Permission = "software:write"
	PermissionWebsiteRead   Permission = "website:read"
	PermissionWebsiteWrite  Permission = "website:write"
	PermissionFirewallRead  Permission = "firewall:read"
	PermissionFirewallWrite Permission = "firewall:write"
	PermissionSSHAccess     Permission = "ssh:access"
	PermissionCronRead      Permission = "cron:read"
	PermissionCronWrite     Permission = "cron:write"
	PermissionFileRead      Permission = "file:read"
	PermissionFileWrite     Permission = "file:write"
)

// RequirePermission 权限验证中间件
func RequirePermission(requiredPermission Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取token信息
		tokenClaims, exists := c.Get(ContextTokenClaims)
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication required",
				"code":  "AUTH_REQUIRED",
			})
			c.Abort()
			return
		}

		claims, ok := tokenClaims.(*utils.Claims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid token claims",
				"code":  "INVALID_CLAIMS",
			})
			c.Abort()
			return
		}

		// 检查用户权限
		if !hasPermission(claims, requiredPermission) {
			c.JSON(http.StatusForbidden, gin.H{
				"error":               "Insufficient permissions",
				"code":                "INSUFFICIENT_PERMISSIONS",
				"required_permission": string(requiredPermission),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireAdmin 需要管理员权限的中间件
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := AuthenticatedUserID(c)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication required",
				"code":  "AUTH_REQUIRED",
			})
			c.Abort()
			return
		}

		database := app.DB()
		if database == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Permission service is unavailable",
				"code":  "PERMISSION_UNAVAILABLE",
			})
			c.Abort()
			return
		}

		var user models.User
		if err := database.Select("id", "is_admin").First(&user, userID).Error; err != nil || !user.IsAdmin {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Administrator access required",
				"code":  "ADMIN_REQUIRED",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// hasPermission 检查用户是否具有特定权限
func hasPermission(claims *utils.Claims, permission Permission) bool {
	// 简化的权限检查逻辑
	// 在实际应用中，应该从数据库查询用户角色和权限

	// 假设用户ID为1的是超级管理员，拥有所有权限
	if claims.Id == 1 {
		return true
	}

	// 这里可以根据实际需求实现更复杂的权限逻辑
	// 例如：从数据库查询用户角色，然后检查角色权限

	return false
}

// AuditLog 审计日志中间件
func AuditLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := auditservice.NewRequestID()
		c.Set(ContextRequestID, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()

		status := c.Writer.Status()
		path := c.Request.URL.Path
		sensitive := isSensitiveOperation(c.Request.Method, path)
		if !shouldPersistAudit(c.Request.Method, path, status, sensitive) {
			return
		}
		manager := auditservice.Default()
		if manager == nil {
			return
		}
		route := c.FullPath()
		if route == "" {
			route = path
		}
		username, _ := c.Get(ContextUsername)
		userID, _ := AuthenticatedUserID(c)
		authMode, _ := c.Get(ContextAuthMode)
		outcome := "success"
		message := ""
		if status >= http.StatusBadRequest {
			outcome = "failure"
			message = http.StatusText(status)
		}
		_, err := manager.Append(auditservice.EventInput{
			RequestID: requestID, EventType: "http",
			Action: strings.ToLower(c.Request.Method) + " " + route,
			Method: c.Request.Method, Route: route, Path: path,
			Status: status, Outcome: outcome, Sensitive: sensitive,
			UserID: userID, Username: valueString(username), AuthMode: valueString(authMode),
			RemoteIP: auditservice.RemoteIP(c.Request), UserAgent: c.GetHeader("User-Agent"),
			ContentLength: c.Request.ContentLength,
			DurationMS:    time.Since(start).Milliseconds(),
			Message:       message,
			CreatedAt:     start,
		})
		if err != nil {
			log.Printf("persist audit event %s: %v", requestID, err)
		}
	}
}

func valueString(value interface{}) string {
	text, _ := value.(string)
	return text
}

func shouldPersistAudit(method, path string, status int, sensitive bool) bool {
	if sensitive || status >= http.StatusBadRequest {
		return true
	}
	if method == http.MethodPost && isReadOnlyPost(path) {
		return false
	}
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func isReadOnlyPost(path string) bool {
	switch path {
	case "/v1/sys/dic/list",
		"/v1/storage/liblist",
		"/v1/storage/rklist",
		"/v1/storage/info",
		"/v1/ftp/list",
		"/v1/ftp/content",
		"/v1/ftp/tree",
		"/v1/soft/list",
		"/v1/soft/exploration",
		"/v1/website/list",
		"/v1/website/info",
		"/v1/safe/rules",
		"/v1/cron/list",
		"/v1/cron/log":
		return true
	default:
		return false
	}
}

// isSensitiveOperation 判断是否为敏感操作
func isSensitiveOperation(method, path string) bool {
	if method != http.MethodGet && strings.HasPrefix(path, "/v1/monitor/") {
		return true
	}
	if method != http.MethodGet && strings.HasPrefix(path, "/v1/soft/services/") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/storage/backups/") &&
		strings.HasSuffix(path, "/delete") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/storage/tasks/") &&
		strings.HasSuffix(path, "/cancel") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/website/certificates/") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/website/certificate-tasks/") &&
		strings.HasSuffix(path, "/cancel") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/website/backups/") &&
		strings.HasSuffix(path, "/delete") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/website/tasks/") &&
		strings.HasSuffix(path, "/cancel") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/sessions/") &&
		strings.HasSuffix(path, "/revoke") {
		return true
	}
	if method == http.MethodPost &&
		strings.HasPrefix(path, "/v1/cron/executions/") &&
		strings.HasSuffix(path, "/cancel") {
		return true
	}
	if method == http.MethodGet &&
		strings.HasPrefix(path, "/v1/storage/backups/") &&
		strings.HasSuffix(path, "/download") {
		return true
	}
	if method == http.MethodGet &&
		strings.HasPrefix(path, "/v1/website/backups/") &&
		strings.HasSuffix(path, "/download") {
		return true
	}
	if method == http.MethodGet &&
		strings.HasPrefix(path, "/v1/soft/tasks/") &&
		strings.HasSuffix(path, "/log/download") {
		return true
	}
	if method == http.MethodGet &&
		strings.HasPrefix(path, "/v1/cron/") &&
		strings.HasSuffix(path, "/log/export") {
		return true
	}
	sensitiveOperations := map[string][]string{
		"POST": {
			"/v1/login",
			"/v1/logout",
			"/v1/sessions/revoke-others",
			"/v1/security/totp/setup",
			"/v1/security/totp/confirm",
			"/v1/security/totp/disable",
			"/v1/security/totp/recovery-codes/regenerate",
			"/v1/sys/updateuser",
			"/v1/sys/resetpassword",
			"/v1/sys/updateport",
			"/v1/sys/update/check",
			"/v1/sys/update/apply",
			"/v1/soft/install",
			"/v1/soft/remove",
			"/v1/storage/addconn",
			"/v1/storage/updateconn",
			"/v1/storage/delconn",
			"/v1/storage/addlib",
			"/v1/storage/dellib",
			"/v1/storage/backups",
			"/v1/storage/restores",
			"/v1/cron/add",
			"/v1/cron/update",
			"/v1/cron/del",
			"/v1/cron/disable",
			"/v1/cron/enable",
			"/v1/cron/run",
			"/v1/cron/log/cleanup",
			"/v1/safe/add",
			"/v1/safe/update",
			"/v1/safe/del",
			"/v1/safe/stop",
			"/v1/safe/blockping",
			"/v1/safe/install",
			"/v1/website/add",
			"/v1/website/del",
			"/v1/website/backups",
			"/v1/website/restores",
			"/v1/website/certificates/acme",
			"/v1/ssh/ticket",
			"/v1/audit/verify",
		},
		"GET": {
			"/v1/ssh/open",
			"/v1/audit/export",
		},
		"DELETE": {
			"/v1/sys/remark/del",
			"/v1/website/del",
			"/v1/safe/del",
		},
	}

	paths, exists := sensitiveOperations[method]
	if !exists {
		return false
	}

	for _, sensitivePath := range paths {
		if strings.HasPrefix(path, sensitivePath) {
			return true
		}
	}

	return false
}
