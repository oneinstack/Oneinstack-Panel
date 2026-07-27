package monitoring

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	monitorservice "oneinstack/internal/services/monitoring"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type silenceRequest struct {
	Minutes int `json:"minutes"`
}

func Summary(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.Summary()
	writeResult(c, result, err)
}

func Metrics(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	from, err := optionalTime(c.Query("from"))
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	to, err := optionalTime(c.Query("to"))
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	if !from.IsZero() && !to.IsZero() &&
		(to.Before(from) || to.Sub(from) > 31*24*time.Hour) {
		writeBadRequest(c, errors.New("指标查询范围必须为 0 到 31 天"))
		return
	}
	limit := optionalInt(c.Query("limit"), 2000)
	result, err := manager.Metrics(from, to, limit)
	writeResult(c, result, err)
}

func ListRules(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.ListRules()
	writeResult(c, result, err)
}

func CreateRule(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	var input monitorservice.RuleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.CreateRule(input)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func UpdateRule(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input monitorservice.RuleInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.UpdateRule(id, input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "告警规则不存在")
		return
	}
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func DeleteRule(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	err := manager.DeleteRule(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "告警规则不存在")
		return
	}
	writeResult(c, gin.H{"deleted": err == nil}, err)
}

func SilenceRule(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var request silenceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeBadRequest(c, err)
		return
	}
	if request.Minutes < 0 || request.Minutes > 30*24*60 {
		writeBadRequest(c, errors.New("静默时间必须在 0 到 43200 分钟之间"))
		return
	}
	var until *time.Time
	if request.Minutes > 0 {
		value := time.Now().UTC().Add(time.Duration(request.Minutes) * time.Minute)
		until = &value
	}
	err := manager.SilenceRule(id, until)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "告警规则不存在")
		return
	}
	writeResult(c, gin.H{"silencedUntil": until}, err)
}

func Events(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	filter := monitorservice.EventFilter{
		Page: optionalInt(c.Query("page"), 1), PageSize: optionalInt(c.Query("pageSize"), 20),
		EventType: strings.TrimSpace(c.Query("eventType")),
		Severity:  strings.TrimSpace(c.Query("severity")),
	}
	if ruleID := strings.TrimSpace(c.Query("ruleId")); ruleID != "" {
		parsed, err := strconv.ParseUint(ruleID, 10, 32)
		if err != nil || parsed == 0 {
			writeBadRequest(c, errors.New("ruleId 必须是正整数"))
			return
		}
		filter.RuleID = uint(parsed)
	}
	if filter.EventType != "" &&
		filter.EventType != "triggered" && filter.EventType != "reminder" && filter.EventType != "resolved" {
		writeBadRequest(c, errors.New("eventType 无效"))
		return
	}
	if filter.Severity != "" &&
		filter.Severity != "info" && filter.Severity != "warning" && filter.Severity != "critical" {
		writeBadRequest(c, errors.New("severity 无效"))
		return
	}
	result, err := manager.Events(filter)
	writeResult(c, result, err)
}

func Deliveries(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != "success" && status != "failed" {
		writeBadRequest(c, errors.New("status 无效"))
		return
	}
	result, err := manager.Deliveries(
		optionalInt(c.Query("page"), 1), optionalInt(c.Query("pageSize"), 20), status,
	)
	writeResult(c, result, err)
}

func ListChannels(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.ListChannels()
	writeResult(c, result, err)
}

func CreateChannel(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	var input monitorservice.ChannelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.CreateChannel(input)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func UpdateChannel(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	var input monitorservice.ChannelInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.UpdateChannel(strings.TrimSpace(c.Param("id")), input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "通知通道不存在")
		return
	}
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func DeleteChannel(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	err := manager.DeleteChannel(strings.TrimSpace(c.Param("id")))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "通知通道不存在")
		return
	}
	writeResult(c, gin.H{"deleted": err == nil}, err)
}

func TestChannel(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
	defer cancel()
	err := manager.TestChannel(ctx, strings.TrimSpace(c.Param("id")))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "通知通道不存在")
		return
	}
	writeResult(c, gin.H{"delivered": err == nil}, err)
}

func managerOrUnavailable(c *gin.Context) (*monitorservice.Manager, bool) {
	manager := monitorservice.Default()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false, "code": "MONITOR_UNAVAILABLE", "message": "监控服务未初始化",
		})
		return nil, false
	}
	return manager, true
}

func parseID(c *gin.Context) (uint, bool) {
	parsed, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || parsed == 0 {
		writeBadRequest(c, errors.New("ID 必须是正整数"))
		return 0, false
	}
	return uint(parsed), true
}

func optionalInt(value string, fallback int) int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
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

func writeResult(c *gin.Context, result interface{}, err error) {
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "监控操作失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func writeBadRequest(c *gin.Context, err error) {
	core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "请求参数无效", err.Error()))
}

func writeNotFound(c *gin.Context, message string) {
	core.HandleErrorWithStatus(c, http.StatusNotFound, core.NewError(core.ErrNotFound, message))
}
