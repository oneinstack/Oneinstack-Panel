package ftp

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

const (
	fileOperationActionKey = "fileOperationAction"
	fileOperationPathKey   = "fileOperationPath"
	fileOperationDoneKey   = "fileOperationDone"
	fileOperationStartKey  = "fileOperationStart"
)

func ListOperations(c *gin.Context) {
	page, err := positiveQueryInt(c.Query("page"), 1, 1, 100000)
	if err != nil {
		handleBadRequest(c, err, "页码无效")
		return
	}
	pageSize, err := positiveQueryInt(c.Query("pageSize"), 20, 1, 100)
	if err != nil {
		handleBadRequest(c, err, "每页数量无效")
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(c.Query("outcome")))
	if outcome != "" && outcome != "success" && outcome != "failure" {
		handleBadRequest(c, fmt.Errorf("outcome must be success or failure"), "操作结果无效")
		return
	}
	for field, value := range map[string]string{
		"q": c.Query("q"), "action": c.Query("action"), "username": c.Query("username"),
	} {
		if len([]rune(strings.TrimSpace(value))) > 128 {
			handleBadRequest(c, fmt.Errorf("%s exceeds 128 characters", field), "查询条件过长")
			return
		}
	}
	manager := auditservice.Default()
	if manager == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "文件操作记录服务未初始化"))
		return
	}
	result, err := manager.List(auditservice.Filter{
		Page:      page,
		PageSize:  pageSize,
		EventType: "file",
		Action:    strings.TrimSpace(c.Query("action")),
		Outcome:   outcome,
		Username:  strings.TrimSpace(c.Query("username")),
		Query:     strings.TrimSpace(c.Query("q")),
	})
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取文件操作记录失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func startFileOperation(c *gin.Context, action, path string) {
	if c == nil {
		return
	}
	c.Set(fileOperationActionKey, strings.TrimSpace(action))
	c.Set(fileOperationPathKey, cleanAuditPath(path))
	c.Set(fileOperationStartKey, time.Now())
}

func finishFileOperation(c *gin.Context, outcome, message string) {
	if c == nil {
		return
	}
	if done, _ := c.Get(fileOperationDoneKey); done == true {
		return
	}
	actionValue, exists := c.Get(fileOperationActionKey)
	if !exists {
		return
	}
	action, _ := actionValue.(string)
	if action == "" {
		return
	}
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	pathValue, _ := c.Get(fileOperationPathKey)
	usernameValue, _ := c.Get(middleware.ContextUsername)
	authModeValue, _ := c.Get(middleware.ContextAuthMode)
	requestIDValue, _ := c.Get(middleware.ContextRequestID)
	userID, _ := middleware.AuthenticatedUserID(c)
	status := c.Writer.Status()
	if status == 0 {
		status = http.StatusOK
	}
	contentLength := c.Request.ContentLength
	if contentLength < 0 {
		contentLength = 0
	}
	durationMS := int64(0)
	if startedValue, exists := c.Get(fileOperationStartKey); exists {
		if startedAt, ok := startedValue.(time.Time); ok {
			durationMS = time.Since(startedAt).Milliseconds()
		}
	}
	_, err := manager.Append(auditservice.EventInput{
		RequestID:     valueString(requestIDValue),
		EventType:     "file",
		Action:        action,
		Method:        c.Request.Method,
		Route:         c.FullPath(),
		Path:          valueString(pathValue),
		Status:        status,
		Outcome:       outcome,
		Sensitive:     true,
		UserID:        userID,
		Username:      valueString(usernameValue),
		AuthMode:      valueString(authModeValue),
		RemoteIP:      auditservice.RemoteIP(c.Request),
		UserAgent:     c.GetHeader("User-Agent"),
		ContentLength: contentLength,
		DurationMS:    durationMS,
		Message:       strings.TrimSpace(message),
		CreatedAt:     time.Now().UTC(),
	})
	if err != nil {
		log.Printf("persist file operation audit: %v", err)
		return
	}
	c.Set(fileOperationDoneKey, true)
	c.Set(middleware.ContextAuditHandled, true)
}

func positiveQueryInt(value string, fallback, minimum, maximum int) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("value must be between %d and %d", minimum, maximum)
	}
	return parsed, nil
}

func cleanAuditPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "/"
	}
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if len(value) > 1024 {
		value = value[:1024]
	}
	return value
}

func valueString(value any) string {
	text, _ := value.(string)
	return text
}
