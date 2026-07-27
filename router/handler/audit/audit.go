package audit

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListEvents(c *gin.Context) {
	filter, err := parseFilter(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_FILTER", err.Error())
		return
	}
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.List(filter)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "AUDIT_QUERY_FAILED", "查询审计日志失败")
		return
	}
	core.HandleSuccess(c, result)
}

func GetEvent(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_ID", "审计日志 ID 无效")
		return
	}
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	event, err := manager.Get(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeError(c, http.StatusNotFound, "AUDIT_EVENT_NOT_FOUND", "审计日志不存在")
		return
	}
	if err != nil {
		writeError(c, http.StatusInternalServerError, "AUDIT_QUERY_FAILED", "查询审计日志失败")
		return
	}
	core.HandleSuccess(c, event)
}

func GetStats(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	stats, err := manager.Stats()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "AUDIT_QUERY_FAILED", "查询审计统计失败")
		return
	}
	core.HandleSuccess(c, gin.H{
		"counts":          stats,
		"retentionDays":   app.ONE_CONFIG.System.AuditRetentionDays,
		"cleanupSchedule": app.ONE_CONFIG.System.AuditCleanupSchedule,
		"exportMaxRows":   app.ONE_CONFIG.System.AuditExportMaxRows,
	})
}

func VerifyChain(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.Verify()
	if err != nil {
		writeError(c, http.StatusInternalServerError, "AUDIT_VERIFY_FAILED", "校验审计链失败")
		return
	}
	core.HandleSuccess(c, result)
}

func ExportEvents(c *gin.Context) {
	filter, err := parseFilter(c)
	if err != nil {
		writeError(c, http.StatusBadRequest, "INVALID_AUDIT_FILTER", err.Error())
		return
	}
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	events, err := manager.Export(filter, app.ONE_CONFIG.System.AuditExportMaxRows)
	if err != nil {
		writeError(c, http.StatusInternalServerError, "AUDIT_EXPORT_FAILED", "导出审计日志失败")
		return
	}

	filename := "oneinstack-audit-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"sequence", "created_at", "request_id", "event_type", "action", "method",
		"route", "path", "status", "outcome", "sensitive", "user_id", "username",
		"auth_mode", "remote_ip", "user_agent", "content_length", "duration_ms",
		"message", "previous_hash", "entry_hash", "chain_version",
	})
	for index := range events {
		_ = writer.Write(eventRow(&events[index]))
	}
	writer.Flush()
}

func managerOrUnavailable(c *gin.Context) (*auditservice.Manager, bool) {
	manager := auditservice.Default()
	if manager == nil {
		writeError(c, http.StatusServiceUnavailable, "AUDIT_UNAVAILABLE", "审计服务未初始化")
		return nil, false
	}
	return manager, true
}

func parseFilter(c *gin.Context) (auditservice.Filter, error) {
	filter := auditservice.Filter{}
	var err error
	if value := strings.TrimSpace(c.Query("page")); value != "" {
		filter.Page, err = strconv.Atoi(value)
		if err != nil || filter.Page < 1 {
			return filter, errors.New("page 必须是正整数")
		}
	}
	if value := strings.TrimSpace(c.Query("pageSize")); value != "" {
		filter.PageSize, err = strconv.Atoi(value)
		if err != nil || filter.PageSize < 1 || filter.PageSize > 100 {
			return filter, errors.New("pageSize 必须在 1 到 100 之间")
		}
	}
	if value := strings.TrimSpace(c.Query("startAt")); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			return filter, errors.New("startAt 必须是 RFC3339 时间")
		}
		filter.StartAt = &parsed
	}
	if value := strings.TrimSpace(c.Query("endAt")); value != "" {
		parsed, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			return filter, errors.New("endAt 必须是 RFC3339 时间")
		}
		filter.EndAt = &parsed
	}
	if filter.StartAt != nil && filter.EndAt != nil && filter.EndAt.Before(*filter.StartAt) {
		return filter, errors.New("endAt 不能早于 startAt")
	}
	filter.Username = strings.TrimSpace(c.Query("username"))
	filter.Outcome = strings.ToLower(strings.TrimSpace(c.Query("outcome")))
	filter.Method = strings.ToUpper(strings.TrimSpace(c.Query("method")))
	filter.Action = strings.TrimSpace(c.Query("action"))
	filter.RemoteIP = strings.TrimSpace(c.Query("remoteIp"))
	filter.EventType = strings.ToLower(strings.TrimSpace(c.Query("eventType")))
	filter.Query = strings.TrimSpace(c.Query("q"))
	for name, value := range map[string]string{
		"username": filter.Username, "action": filter.Action, "remoteIp": filter.RemoteIP,
		"eventType": filter.EventType, "q": filter.Query,
	} {
		if len(value) > 100 {
			return filter, fmt.Errorf("%s 最长为 100 个字符", name)
		}
	}
	if filter.Outcome != "" && filter.Outcome != "success" && filter.Outcome != "failure" {
		return filter, errors.New("outcome 必须是 success 或 failure")
	}
	if value := strings.TrimSpace(c.Query("sensitive")); value != "" {
		parsed, parseErr := strconv.ParseBool(value)
		if parseErr != nil {
			return filter, errors.New("sensitive 必须是 true 或 false")
		}
		filter.Sensitive = &parsed
	}
	if value := strings.TrimSpace(c.Query("status")); value != "" {
		filter.StatusCode, err = strconv.Atoi(value)
		if err != nil || filter.StatusCode < 100 || filter.StatusCode > 599 {
			return filter, errors.New("status 必须是有效的 HTTP 状态码")
		}
	}
	return filter, nil
}

func eventRow(event *models.AuditEvent) []string {
	return []string{
		strconv.FormatUint(event.Sequence, 10),
		event.CreatedAt.UTC().Format(time.RFC3339Nano),
		csvSafe(event.RequestID), csvSafe(event.EventType), csvSafe(event.Action),
		csvSafe(event.Method), csvSafe(event.Route), csvSafe(event.Path),
		strconv.Itoa(event.Status), csvSafe(event.Outcome),
		strconv.FormatBool(event.Sensitive), strconv.FormatInt(event.UserID, 10),
		csvSafe(event.Username), csvSafe(event.AuthMode), csvSafe(event.RemoteIP),
		csvSafe(event.UserAgent), strconv.FormatInt(event.ContentLength, 10),
		strconv.FormatInt(event.DurationMS, 10), csvSafe(event.Message),
		event.PreviousHash, event.EntryHash, strconv.Itoa(int(event.ChainVersion)),
	}
}

func csvSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"code":    code,
		"message": message,
	})
}
