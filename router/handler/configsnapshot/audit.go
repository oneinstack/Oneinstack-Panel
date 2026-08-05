package configsnapshot

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func RecordAudit(c *gin.Context, snapshot *models.ConfigurationSnapshot, status, message string) {
	if snapshot == nil || c == nil {
		return
	}
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	username, _ := c.Get(middleware.ContextUsername)
	requestID, _ := c.Get(middleware.ContextRequestID)
	outcome := "success"
	if strings.HasSuffix(status, "failed") || status == models.ConfigurationSnapshotStatusFailed {
		outcome = "failure"
	}
	_, _ = manager.Append(auditservice.EventInput{
		RequestID: valueString(requestID), EventType: "configuration", Action: "config.snapshot." + snapshot.Operation,
		Method: c.Request.Method, Route: c.FullPath(), Path: c.Request.URL.Path, Status: http.StatusOK,
		Outcome: outcome, Sensitive: true, UserID: userID, Username: valueString(username),
		RemoteIP: auditservice.RemoteIP(c.Request), UserAgent: c.GetHeader("User-Agent"),
		Message:   fmt.Sprintf("snapshot=%s resource=%s/%s status=%s %s", snapshot.ID, snapshot.ResourceType, snapshot.ResourceID, status, strings.TrimSpace(message)),
		CreatedAt: time.Now().UTC(),
	})
}

func valueString(value any) string { text, _ := value.(string); return text }
