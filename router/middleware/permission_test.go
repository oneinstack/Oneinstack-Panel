package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	auditservice "oneinstack/internal/services/audit"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestAuditMiddlewarePersistsSanitizedOperation(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:middleware-audit?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.AuditEvent{}, &models.AuditCheckpoint{}, &models.AuditChainState{}); err != nil {
		t.Fatal(err)
	}
	manager, err := auditservice.ConfigureDefault(database, bytes.Repeat([]byte{0x3c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditservice.ClearDefault(manager) })

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(AuditLog())
	engine.POST("/v1/test", func(c *gin.Context) {
		c.Set(ContextUsername, "admin")
		c.Set(ContextUserID, int64(7))
		c.Set(ContextAuthMode, AuthModeCookie)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/test?token=do-not-store", strings.NewReader(`{"password":"do-not-store"}`))
	request.RemoteAddr = "192.0.2.44:54321"
	request.Header.Set("User-Agent", "audit-test")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("X-Request-ID") == "" {
		t.Fatal("audit request ID response header was not set")
	}
	var event models.AuditEvent
	if err := database.First(&event).Error; err != nil {
		t.Fatal(err)
	}
	if event.Path != "/v1/test" || event.Username != "admin" || event.UserID != 7 ||
		event.RemoteIP != "192.0.2.44" || event.ContentLength == 0 {
		t.Fatalf("unexpected audit event: %#v", event)
	}
	encoded := strings.Join([]string{event.Path, event.Route, event.Action, event.Message}, " ")
	if strings.Contains(encoded, "do-not-store") {
		t.Fatalf("request query or body leaked into audit record: %#v", event)
	}
}

func TestAuditMiddlewareSkipsSuccessfulReadButCapturesFailure(t *testing.T) {
	if shouldPersistAudit(http.MethodGet, "/v1/sys/monitor", http.StatusOK, false) {
		t.Fatal("successful non-sensitive reads should not fill the operation audit log")
	}
	if !shouldPersistAudit(http.MethodGet, "/v1/sys/monitor", http.StatusUnauthorized, false) {
		t.Fatal("failed reads must be audited")
	}
	if !shouldPersistAudit(http.MethodGet, "/v1/audit/export", http.StatusOK, true) {
		t.Fatal("sensitive reads must be audited")
	}
	if shouldPersistAudit(http.MethodPost, "/v1/website/list", http.StatusOK, false) {
		t.Fatal("legacy read-only POST routes should not fill the operation audit log")
	}
	if !isSensitiveOperation(http.MethodPost, "/v1/monitor/rules") ||
		!isSensitiveOperation(http.MethodPost, "/v1/monitor/channels/channel-1/delete") {
		t.Fatal("monitoring configuration mutations must be treated as sensitive operations")
	}
	if !isSensitiveOperation(http.MethodPost, "/v1/soft/services/nginx/config/apply") ||
		!isSensitiveOperation(http.MethodPost, "/v1/soft/services/nginx/actions") {
		t.Fatal("component service and configuration mutations must be sensitive operations")
	}
	if isSensitiveOperation(http.MethodGet, "/v1/monitor/metrics") {
		t.Fatal("successful metric reads should not fill the operation audit log")
	}
	if !isSensitiveOperation(http.MethodPost, "/v1/website/backups") ||
		!isSensitiveOperation(http.MethodPost, "/v1/website/restores") ||
		!isSensitiveOperation(http.MethodPost, "/v1/website/tasks/task-1/cancel") ||
		!isSensitiveOperation(http.MethodGet, "/v1/website/backups/backup-1/download") {
		t.Fatal("website backup mutations, cancellation, and downloads must be sensitive")
	}
}

func TestBuildAuthorizationMatrixIncludesFullMenuForScopedRole(t *testing.T) {
	matrix := BuildAuthorizationMatrix(&accessservice.UserAccess{
		UserID:   7,
		Username: "website-admin",
		Roles: []accessservice.RoleSummary{
			{Code: accessservice.RoleWebsiteAdmin, Name: "网站管理员"},
		},
		Permissions: []string{
			accessservice.PermissionDashboardRead,
			accessservice.PermissionRuntimeLogRead,
			accessservice.PermissionFileRead,
			accessservice.PermissionFileWrite,
			accessservice.PermissionSoftwareRead,
			accessservice.PermissionServiceRead,
			accessservice.PermissionServiceWrite,
			accessservice.PermissionWebsiteRead,
			accessservice.PermissionWebsiteWrite,
			accessservice.PermissionWebsiteApproval,
			accessservice.PermissionApprovalRequest,
		},
		PermissionSet: map[string]struct{}{
			accessservice.PermissionDashboardRead:   {},
			accessservice.PermissionRuntimeLogRead:  {},
			accessservice.PermissionFileRead:        {},
			accessservice.PermissionFileWrite:       {},
			accessservice.PermissionSoftwareRead:    {},
			accessservice.PermissionServiceRead:     {},
			accessservice.PermissionServiceWrite:    {},
			accessservice.PermissionWebsiteRead:     {},
			accessservice.PermissionWebsiteWrite:    {},
			accessservice.PermissionWebsiteApproval: {},
			accessservice.PermissionApprovalRequest: {},
		},
	})

	expectedVisible := []string{
		"dashboard",
		"website",
		"file",
		"runtimeLog",
		"software",
		"logout",
	}
	for _, key := range expectedVisible {
		if !matrix.Menu[key] {
			t.Fatalf("menu %q should be visible for scoped role", key)
		}
	}

	expectedHidden := []string{
		"database",
		"audit",
		"approval",
		"monitoring",
		"cron",
		"security",
		"panelSettings",
		"userManagement",
		"systemAccess",
	}
	for _, key := range expectedHidden {
		if matrix.Menu[key] {
			t.Fatalf("menu %q should stay hidden for scoped role", key)
		}
	}
}

func TestBuildAuthorizationMatrixGrantsAdminMenusToSuperAdmin(t *testing.T) {
	matrix := BuildAuthorizationMatrix(&accessservice.UserAccess{
		UserID:       1,
		Username:     "root",
		IsSuperAdmin: true,
	})

	for _, key := range []string{"security", "panelSettings", "userManagement", "systemAccess"} {
		if !matrix.Menu[key] {
			t.Fatalf("menu %q should be visible for super admin", key)
		}
	}
}
