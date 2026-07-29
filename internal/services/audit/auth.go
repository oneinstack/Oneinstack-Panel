package audit

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func RecordAuthEvent(
	c *gin.Context,
	action, username string,
	userID int64,
	status int,
	outcome, authMode, message string,
) {
	manager := Default()
	if manager == nil || c == nil {
		return
	}
	requestID, _ := c.Get("requestId")
	requestText, _ := requestID.(string)
	_, _ = manager.Append(EventInput{
		RequestID:     requestText,
		EventType:     "auth",
		Action:        action,
		Method:        c.Request.Method,
		Route:         c.FullPath(),
		Path:          c.Request.URL.Path,
		Status:        status,
		Outcome:       outcome,
		Sensitive:     true,
		UserID:        userID,
		Username:      username,
		AuthMode:      authMode,
		RemoteIP:      RemoteIP(c.Request),
		UserAgent:     c.GetHeader("User-Agent"),
		ContentLength: c.Request.ContentLength,
		Message:       message,
		CreatedAt:     time.Now().UTC(),
	})
}

func OutcomeForStatus(status int) string {
	if status >= http.StatusBadRequest {
		return "failure"
	}
	return "success"
}
