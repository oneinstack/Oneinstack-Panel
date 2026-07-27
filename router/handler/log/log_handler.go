package log

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	logservice "oneinstack/internal/services/log"

	"github.com/gin-gonic/gin"
)

func ListRuntimeLogs(c *gin.Context) {
	manager, ok := runtimeManager(c)
	if !ok {
		return
	}
	filter, err := runtimeFilter(c)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.Query(filter)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "查询运行日志失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func RuntimeLogStats(c *gin.Context) {
	manager, ok := runtimeManager(c)
	if !ok {
		return
	}
	result, err := manager.Stats()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "查询运行日志统计失败"))
		return
	}
	core.HandleSuccess(c, result)
}

// StreamRuntimeLogs implements an authenticated SSE stream. Subscribing before
// the cursor backfill closes the query/subscribe race; duplicate entries are
// removed using the monotonically increasing persisted ID.
func StreamRuntimeLogs(c *gin.Context) {
	manager, ok := runtimeManager(c)
	if !ok {
		return
	}
	filter, err := runtimeFilter(c)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	if filter.BeforeID > 0 {
		writeBadRequest(c, errors.New("实时订阅不支持 beforeId"))
		return
	}
	if header := strings.TrimSpace(c.GetHeader("Last-Event-ID")); header != "" {
		lastEventID, parseErr := parseUint64(header, "Last-Event-ID")
		if parseErr != nil {
			writeBadRequest(c, parseErr)
			return
		}
		if lastEventID > filter.AfterID {
			filter.AfterID = lastEventID
		}
	}
	filter.Limit = 1000
	subscription, unsubscribe := manager.Subscribe(512)
	defer unsubscribe()

	firstPage, err := manager.Query(filter)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "订阅运行日志失败"))
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-store")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	cursor := filter.AfterID
	for _, entry := range firstPage.Items {
		if entry.ID <= cursor {
			continue
		}
		if err := writeSSE(c, entry.ID, "log", entry); err != nil {
			return
		}
		cursor = entry.ID
	}
	if firstPage.HasMore {
		// Closing intentionally lets EventSource reconnect with Last-Event-ID,
		// bounding each catch-up response without losing persisted entries.
		return
	}
	c.Writer.Flush()

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case entry, open := <-subscription:
			if !open {
				return
			}
			if entry.ID <= cursor || !logservice.Matches(entry, filter) {
				continue
			}
			if err := writeSSE(c, entry.ID, "log", entry); err != nil {
				return
			}
			cursor = entry.ID
			c.Writer.Flush()
		case <-heartbeat.C:
			if _, err := c.Writer.WriteString(": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func writeSSE(c *gin.Context, id uint64, event string, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", id, event, encoded); err != nil {
		return err
	}
	return nil
}

func runtimeManager(c *gin.Context) (*logservice.Manager, bool) {
	manager := logservice.RuntimeDefault()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"code":    "RUNTIME_LOG_UNAVAILABLE",
			"message": "运行日志服务未初始化",
		})
		return nil, false
	}
	return manager, true
}

func runtimeFilter(c *gin.Context) (logservice.QueryFilter, error) {
	afterID, err := optionalUint64(c.Query("afterId"), "afterId")
	if err != nil {
		return logservice.QueryFilter{}, err
	}
	beforeID, err := optionalUint64(c.Query("beforeId"), "beforeId")
	if err != nil {
		return logservice.QueryFilter{}, err
	}
	limit := 200
	if value := strings.TrimSpace(c.Query("limit")); value != "" {
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			return logservice.QueryFilter{}, errors.New("limit 必须是 1 到 1000 的整数")
		}
	}
	startAt, err := optionalTime(c.Query("startAt"))
	if err != nil {
		return logservice.QueryFilter{}, err
	}
	endAt, err := optionalTime(c.Query("endAt"))
	if err != nil {
		return logservice.QueryFilter{}, err
	}
	filter := logservice.QueryFilter{
		AfterID: afterID, BeforeID: beforeID, Limit: limit,
		Level:   strings.ToLower(strings.TrimSpace(c.Query("level"))),
		Source:  strings.ToLower(strings.TrimSpace(c.Query("source"))),
		Query:   strings.TrimSpace(c.Query("q")),
		StartAt: startAt, EndAt: endAt,
	}
	if err := logservice.ValidateFilter(&filter); err != nil {
		return logservice.QueryFilter{}, err
	}
	return filter, nil
}

func optionalUint64(value, field string) (uint64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	return parseUint64(value, field)
}

func parseUint64(value, field string) (uint64, error) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s 必须是非负整数", field)
	}
	return parsed, nil
}

func optionalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, errors.New("时间必须使用 RFC3339 格式")
	}
	return parsed, nil
}

func writeBadRequest(c *gin.Context, err error) {
	core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "请求参数无效", err.Error()))
}
