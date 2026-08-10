package middleware

import (
	"errors"
	"log"
	"net/http"
	"oneinstack/app"
	"oneinstack/core"
	accessservice "oneinstack/internal/services/access"
	auditservice "oneinstack/internal/services/audit"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	ContextUserAccess   = "userAccess"
	ContextAuditHandled = "auditHandled"
)

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
		key: "container",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return has(accessservice.PermissionContainerRead) || has(accessservice.PermissionContainerWrite)
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
		key: "terminal",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return app.ONE_CONFIG.System.TerminalEnabled &&
				has(accessservice.PermissionTerminalAccess)
		},
	},
	{
		key: "bastion",
		visible: func(has func(string) bool, access *accessservice.UserAccess) bool {
			return app.ONE_CONFIG.Bastion.Enabled &&
				(has(accessservice.PermissionBastionRead) || has(accessservice.PermissionBastionWrite))
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
		writeAPIError(c, http.StatusForbidden, core.ErrAdminRequired, "当前操作需要管理员权限", "当前用户未加载有效的管理员权限，无法访问该接口。")
		return
	}
	writeAPIError(c, http.StatusForbidden, core.ErrInsufficientPermissions, "当前用户没有执行此操作的权限", "当前接口要求权限："+requiredPermission+"。")
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
			writeAPIError(c, http.StatusServiceUnavailable, core.ErrPermissionUnavailable, "权限服务不可用，请稍后重试", "服务端数据库未初始化，无法加载当前用户权限。")
			return
		}
		access, err := accessservice.NewService(database).LoadUserAccess(userID)
		if err != nil {
			mode, _ := c.Get(ContextAuthMode)
			if mode == AuthModeTicket || errors.Is(err, gorm.ErrRecordNotFound) {
				c.Next()
				return
			}
			writeAPIError(c, http.StatusServiceUnavailable, core.ErrPermissionUnavailable, "权限服务不可用，请稍后重试", "读取当前用户权限失败："+err.Error())
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
			writeAPIError(c, http.StatusForbidden, core.ErrAdminRequired, "当前操作需要超级管理员权限", "只有超级管理员可以访问该接口。")
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
	actions := map[string]bool{
		"website.delete":              has(accessservice.PermissionWebsiteWrite),
		"website.restore":             has(accessservice.PermissionWebsiteWrite),
		"database.restore":            has(accessservice.PermissionDatabaseWrite),
		"database.connection.delete":  has(accessservice.PermissionDatabaseWrite),
		"audit.export":                has(accessservice.PermissionAuditExport),
		"audit.verify":                has(accessservice.PermissionAuditVerify),
		"database.credential.reveal":  has(accessservice.PermissionDatabaseWrite),
		"certificate.issue":           has(accessservice.PermissionWebsiteWrite),
		"file.create":                 has(accessservice.PermissionFileCreate),
		"file.edit":                   has(accessservice.PermissionFileEdit),
		"file.move":                   has(accessservice.PermissionFileMove),
		"file.delete":                 has(accessservice.PermissionFileDelete),
		"file.modify":                 has(accessservice.PermissionFileModify),
		"file.archive":                has(accessservice.PermissionFileArchive),
		"file.share.create":           has(accessservice.PermissionFileShare),
		"file.share.revoke":           has(accessservice.PermissionFileShare),
		"container.terminal":          has(accessservice.PermissionContainerTerminal),
		"container.force_action":      has(accessservice.PermissionContainerForceAction),
		"container.dangerous.cleanup": has(accessservice.PermissionContainerDangerousCleanup),
	}
	for operation, permission := range accessservice.OperationPermissions() {
		actions[operation] = has(permission)
	}
	return AuthorizationMatrix{
		Menu:    menu,
		Actions: actions,
		ApprovalPolicies: map[string]bool{
			"website.delete":             true,
			"website.restore":            true,
			"database.restore":           true,
			"database.connection.delete": true,
			"certificate.issue":          true,
			"certificate.renew":          true,
			"certificate.disable":        true,
			"database.credential.reveal": true,
			"file.delete":                true,
			"file.trash.empty":           true,
			"file.trash.delete":          true,
		},
		Scopes: map[string]map[string]bool{
			"dashboard": {
				"read": has(accessservice.PermissionDashboardRead),
			},
			"runtimeLog": {
				"read": has(accessservice.PermissionRuntimeLogRead),
			},
			"software": {
				"read":         has(accessservice.PermissionSoftwareRead),
				"write":        has(accessservice.PermissionSoftwareWrite),
				"serviceRead":  has(accessservice.PermissionServiceRead),
				"serviceWrite": has(accessservice.PermissionServiceWrite),
			},
			"security": {
				"read":  has(accessservice.PermissionSecurityRead),
				"write": has(accessservice.PermissionSecurityWrite),
			},
			"cron": {
				"read":  has(accessservice.PermissionCronRead),
				"write": has(accessservice.PermissionCronWrite),
			},
			"monitoring": {
				"read":  has(accessservice.PermissionMonitoringRead),
				"write": has(accessservice.PermissionMonitoringWrite),
			},
			"system": {
				"read":  has(accessservice.PermissionSystemRead),
				"write": has(accessservice.PermissionSystemWrite),
			},
			"configuration": {
				"read":  has(accessservice.PermissionConfigSnapshotRead),
				"write": has(accessservice.PermissionConfigSnapshotWrite),
			},
			"audit": {
				"read":   has(accessservice.PermissionAuditRead),
				"export": has(accessservice.PermissionAuditExport),
				"verify": has(accessservice.PermissionAuditVerify),
			},
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
				"request": has(accessservice.PermissionApprovalRequest),
				"review":  has(accessservice.PermissionApprovalReview),
				"execute": has(accessservice.PermissionApprovalExecute),
			},
			"task": {
				"readSelf":   has(accessservice.PermissionTaskReadSelf),
				"readAll":    has(accessservice.PermissionTaskReadAll),
				"cancelSelf": has(accessservice.PermissionTaskCancelSelf),
			},
			"file": {
				"read":          has(accessservice.PermissionFileRead),
				"create":        has(accessservice.PermissionFileCreate),
				"edit":          has(accessservice.PermissionFileEdit),
				"move":          has(accessservice.PermissionFileMove),
				"delete":        has(accessservice.PermissionFileDelete),
				"modify":        has(accessservice.PermissionFileModify),
				"archive":       has(accessservice.PermissionFileArchive),
				"share":         has(accessservice.PermissionFileShare),
				"scopeRoot":     has(accessservice.PermissionFileScopeRoot),
				"scopeWebsites": has(accessservice.PermissionFileScopeWebsites),
				"scopeBackups":  has(accessservice.PermissionFileScopeBackups),
			},
			"container": {
				"read":             has(accessservice.PermissionContainerRead),
				"write":            has(accessservice.PermissionContainerWrite),
				"delete":           has(accessservice.PermissionContainerDelete),
				"terminal":         has(accessservice.PermissionContainerTerminal),
				"logsRead":         has(accessservice.PermissionContainerLogsRead),
				"imageWrite":       has(accessservice.PermissionContainerImageWrite),
				"networkWrite":     has(accessservice.PermissionContainerNetworkWrite),
				"volumeWrite":      has(accessservice.PermissionContainerVolumeWrite),
				"composeWrite":     has(accessservice.PermissionContainerComposeWrite),
				"registryWrite":    has(accessservice.PermissionContainerRegistryWrite),
				"configWrite":      has(accessservice.PermissionContainerConfigWrite),
				"runtimeInstall":   has(accessservice.PermissionContainerRuntimeInstall),
				"dangerousCleanup": has(accessservice.PermissionContainerDangerousCleanup),
				"forceAction":      has(accessservice.PermissionContainerForceAction),
			},
			"bastion": {
				"read":         has(accessservice.PermissionBastionRead),
				"write":        has(accessservice.PermissionBastionWrite),
				"identityRead": has(accessservice.PermissionBastionIdentityRead),
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

		if handled, _ := c.Get(ContextAuditHandled); handled == true {
			return
		}
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
		"/v1/ftp/search",
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
	if method != http.MethodGet && strings.HasPrefix(path, "/v1/bastion/") {
		return true
	}
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
		strings.HasPrefix(path, "/v1/sys/backups/") &&
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
			"/v1/sys/backups",
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
			"/v1/safe/rules/state",
			"/v1/safe/rules/batch",
			"/v1/safe/rules/cleanup",
			"/v1/safe/rules/import",
			"/v1/safe/forwards/add",
			"/v1/safe/forwards/update",
			"/v1/safe/forwards/del",
			"/v1/safe/forwards/state",
			"/v1/safe/auto-block",
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
