package middleware

import (
	"errors"
	"log"
	"net/http"
	"oneinstack/app"
	accessservice "oneinstack/internal/services/access"
	auditservice "oneinstack/internal/services/audit"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const ContextUserAccess = "userAccess"

type AuthorizationMatrix struct {
	Menu             map[string]bool            `json:"menu"`
	Actions          map[string]bool            `json:"actions"`
	ApprovalPolicies map[string]bool            `json:"approvalPolicies"`
	Scopes           map[string]map[string]bool `json:"scopes"`
}

type menuVisibilityRule struct {
	key     string
	visible func(has func(string) bool, access *accessservice.UserAccess) bool
}

var authorizationMenuRules = []menuVisibilityRule{
	{
		key: "dashboard",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return access != nil && (access.IsSuperAdmin || has(accessservice.PermissionDashboardRead))
		},
	},
	{
		key: "website",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionWebsiteRead) || has(accessservice.PermissionWebsiteWrite)
		},
	},
	{
		key: "database",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionDatabaseRead) || has(accessservice.PermissionDatabaseWrite)
		},
	},
	{
		key: "monitoring",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionMonitoringRead) || has(accessservice.PermissionMonitoringWrite)
		},
	},
	{
		key: "security",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionSecurityRead) || has(accessservice.PermissionSecurityWrite)
		},
	},
	{
		key: "file",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionFileRead) || has(accessservice.PermissionFileWrite)
		},
	},
	{
		key: "audit",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionAuditRead)
		},
	},
	{
		key: "runtimeLog",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionRuntimeLogRead)
		},
	},
	{
		key: "cron",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionCronRead) || has(accessservice.PermissionCronWrite)
		},
	},
	{
		key: "software",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionSoftwareRead) ||
				has(accessservice.PermissionSoftwareWrite) ||
				has(accessservice.PermissionServiceRead) ||
				has(accessservice.PermissionServiceWrite)
		},
	},
	{
		key: "panelSettings",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return access != nil && access.IsSuperAdmin
		},
	},
	{
		key: "userManagement",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return access != nil && access.IsSuperAdmin
		},
	},
	{
		key: "approval",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionApprovalRead)
		},
	},
	{
		key: "logout",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return access != nil
		},
	},
	{
		key: "systemAccess",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionSystemRead) ||
				has(accessservice.PermissionSystemWrite) ||
				has(accessservice.PermissionTerminalAccess)
		},
	},
}

// RequirePermission 权限验证中间件
func RequirePermission(requiredPermission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		access, ok := UserAccess(c)
		if !ok || !access.HasPermission(requiredPermission) {
			writePermissionDenied(c, access, requiredPermission)
			return
		}
		c.Next()
	}
}

func writePermissionDenied(c *gin.Context, access *accessservice.UserAccess, requiredPermission string) {
	if access == nil || (!access.IsSuperAdmin && len(access.Roles) == 0 && len(access.Permissions) == 0) {
		c.JSON(http.StatusForbidden, gin.H{
			"error": "Administrator access required",
			"code":  "ADMIN_REQUIRED",
		})
		c.Abort()
		return
	}
	c.JSON(http.StatusForbidden, gin.H{
		"error":               "Insufficient permissions",
		"code":                "INSUFFICIENT_PERMISSIONS",
		"required_permission": requiredPermission,
	})
	c.Abort()
}

func RequireAnyPermission(requiredPermissions ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		access, ok := UserAccess(c)
		if ok {
			for _, permission := range requiredPermissions {
				if access.HasPermission(permission) {
					c.Next()
					return
				}
			}
		}
		writePermissionDenied(c, access, strings.Join(requiredPermissions, ","))
	}
}

func LoadAuthorizationContext() gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := AuthenticatedUserID(c)
		if !ok {
			c.Next()
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
		access, err := accessservice.NewService(database).LoadUserAccess(userID)
		if err != nil {
			mode, _ := c.Get(ContextAuthMode)
			if mode == AuthModeTicket || errors.Is(err, gorm.ErrRecordNotFound) {
				c.Next()
				return
			}
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "Permission service is unavailable",
				"code":  "PERMISSION_UNAVAILABLE",
			})
			c.Abort()
			return
		}
		c.Set(ContextUserAccess, access)
		c.Next()
	}
}

func RequireSuperAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		access, ok := UserAccess(c)
		if !ok || !access.IsSuperAdmin {
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

// RequireAdmin keeps compatibility with existing route intent and now
// explicitly means "super administrator only".
func RequireAdmin() gin.HandlerFunc {
	return RequireSuperAdmin()
}

func UserAccess(c *gin.Context) (*accessservice.UserAccess, bool) {
	value, exists := c.Get(ContextUserAccess)
	if !exists {
		return nil, false
	}
	access, ok := value.(*accessservice.UserAccess)
	return access, ok && access != nil
}

func BuildAuthorizationMatrix(access *accessservice.UserAccess) AuthorizationMatrix {
	has := func(code string) bool {
		return access != nil && access.HasPermission(code)
	}
	menu := make(map[string]bool, len(authorizationMenuRules))
	for _, rule := range authorizationMenuRules {
		menu[rule.key] = rule.visible(has, access)
	}
	return AuthorizationMatrix{
		Menu: menu,
		Actions: map[string]bool{
			"website.delete":             has(accessservice.PermissionWebsiteWrite),
			"website.restore":            has(accessservice.PermissionWebsiteWrite),
			"database.restore":           has(accessservice.PermissionDatabaseWrite),
			"database.connection.delete": has(accessservice.PermissionDatabaseWrite),
			"audit.export":               has(accessservice.PermissionAuditExport),
			"database.credential.reveal": has(accessservice.PermissionDatabaseWrite),
			"certificate.issue":          has(accessservice.PermissionWebsiteWrite),
		},
		ApprovalPolicies: map[string]bool{
			"website.delete":             true,
			"website.restore":            true,
			"database.restore":           true,
			"database.connection.delete": true,
			"certificate.issue":          true,
			"certificate.renew":          true,
			"certificate.disable":        true,
			"database.credential.reveal": true,
		},
		Scopes: map[string]map[string]bool{
			"website": {
				"read":            has(accessservice.PermissionWebsiteRead),
				"write":           has(accessservice.PermissionWebsiteWrite),
				"approvalRequest": has(accessservice.PermissionWebsiteApproval) || has(accessservice.PermissionApprovalRequest),
			},
			"database": {
				"read":            has(accessservice.PermissionDatabaseRead),
				"write":           has(accessservice.PermissionDatabaseWrite),
				"approvalRequest": has(accessservice.PermissionDatabaseApproval) || has(accessservice.PermissionApprovalRequest),
			},
			"approval": {
				"read":    has(accessservice.PermissionApprovalRead),
				"review":  has(accessservice.PermissionApprovalReview),
				"execute": has(accessservice.PermissionApprovalExecute),
			},
		},
	}
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
