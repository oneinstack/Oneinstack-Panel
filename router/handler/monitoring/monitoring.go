package monitoring

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	"oneinstack/internal/i18n"
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

func ServiceHealth(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	includeNotInstalled := strings.EqualFold(strings.TrimSpace(c.Query("includeNotInstalled")), "true")
	result, err := manager.ListServiceHealth(includeNotInstalled)
	writeResult(c, result, err)
}

func CheckServiceHealth(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if err := manager.CheckServiceHealth(ctx); err != nil {
		writeResult(c, nil, err)
		return
	}
	result, err := manager.ListServiceHealth(false)
	writeResult(c, result, err)
}

func SilenceServiceHealth(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
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
	err := manager.SilenceServiceHealth(strings.TrimSpace(c.Param("component")), until)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "组件健康状态不存在")
		return
	}
	writeResult(c, gin.H{"silencedUntil": until}, err)
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

func History(c *gin.Context) {
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
	from, to, err = resolveHistoryRange(from, to, time.Now().UTC())
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.History(from, to)
	writeResult(c, result, err)
}

func ListRules(c *gin.Context) {
	manager, ok := managerOrUnavailable(c)
	if !ok {
		return
	}
	result, err := manager.ListRules()
	if err == nil {
		locale := c.GetString("locale")
		for index := range result {
			result[index].SeverityName = i18n.LocalizeMonitorSeverity(locale, result[index].Severity)
		}
	}
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
		EventType:    strings.TrimSpace(c.Query("eventType")),
		Severity:     strings.TrimSpace(c.Query("severity")),
		ResourceType: strings.TrimSpace(c.Query("resourceType")),
		ResourceID:   strings.ToLower(strings.TrimSpace(c.Query("resourceId"))),
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
	if filter.ResourceType != "" && filter.ResourceType != "component_service" {
		writeBadRequest(c, errors.New("resourceType 无效"))
		return
	}
	if len(filter.ResourceID) > 64 {
		writeBadRequest(c, errors.New("resourceId 过长"))
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
		core.HandleErrorWithStatus(c, http.StatusServiceUnavailable,
			core.NewErrorWithDetail(core.ErrServiceUnavailable, "监控服务不可用，请稍后重试", "监控服务未初始化，无法读取监控指标或执行监控操作。"))
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

func resolveHistoryRange(from, to, now time.Time) (time.Time, time.Time, error) {
	now = now.UTC().Truncate(time.Second)
	from = from.UTC().Truncate(time.Second)
	to = to.UTC().Truncate(time.Second)
	switch {
	case from.IsZero() && to.IsZero():
		to = now
		from = to.Add(-24 * time.Hour)
	case from.IsZero():
		from = to.Add(-24 * time.Hour)
	case to.IsZero():
		to = now
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, errors.New("历史样本查询结束时间不能早于开始时间")
	}
	if to.Sub(from) > 31*24*time.Hour {
		return time.Time{}, time.Time{}, errors.New("历史样本查询范围不能超过 31 天")
	}
	return from, to, nil
}

func writeResult(c *gin.Context, result interface{}, err error) {
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, monitoringOperationMessage(c)))
		return
	}
	if history, ok := result.(*monitorservice.HistoryResponse); ok && history != nil {
		for index := range history.Series {
			history.Series[index].Label = i18n.LocalizeBusinessText(c.GetString("locale"), history.Series[index].Label)
		}
	}
	core.HandleSuccess(c, result)
}

func writeBadRequest(c *gin.Context, err error) {
	core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, monitoringParameterMessage(c), err.Error()))
}

func monitoringOperationMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/monitor/summary":
		return "读取监控摘要失败"
	case "/v1/monitor/services":
		return "读取组件健康状态失败"
	case "/v1/monitor/services/check":
		return "执行组件健康检查失败"
	case "/v1/monitor/services/:component/silence":
		return "更新组件健康静默状态失败"
	case "/v1/monitor/metrics":
		return "读取监控指标失败"
	case "/v1/monitor/history":
		return "读取监控历史失败"
	case "/v1/monitor/rules":
		if c.Request.Method == http.MethodDelete {
			return "删除告警规则失败"
		}
		return "读取告警规则列表失败"
	case "/v1/monitor/rules/:id", "/v1/monitor/rules/:id/delete":
		return "删除告警规则失败"
	case "/v1/monitor/rules/:id/silence":
		return "更新告警规则静默状态失败"
	case "/v1/monitor/events":
		return "读取告警事件失败"
	case "/v1/monitor/deliveries":
		return "读取通知投递记录失败"
	case "/v1/monitor/channels":
		return "读取通知通道列表失败"
	case "/v1/monitor/channels/:id", "/v1/monitor/channels/:id/delete":
		return "删除通知通道失败"
	case "/v1/monitor/channels/:id/test":
		return "发送测试通知失败"
	default:
		return "处理监控请求失败"
	}
}

func monitoringParameterMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/monitor/services/:component/silence":
		return "组件健康静默参数无效"
	case "/v1/monitor/metrics":
		return "监控指标查询参数无效"
	case "/v1/monitor/history":
		return "监控历史查询参数无效"
	case "/v1/monitor/rules":
		return "告警规则参数无效"
	case "/v1/monitor/rules/:id", "/v1/monitor/rules/:id/update":
		return "告警规则更新参数无效"
	case "/v1/monitor/rules/:id/silence":
		return "告警规则静默参数无效"
	case "/v1/monitor/events":
		return "告警事件查询参数无效"
	case "/v1/monitor/deliveries":
		return "通知投递记录查询参数无效"
	case "/v1/monitor/channels":
		return "通知通道参数无效"
	case "/v1/monitor/channels/:id", "/v1/monitor/channels/:id/update":
		return "通知通道更新参数无效"
	default:
		return "监控请求参数无效"
	}
}

func writeNotFound(c *gin.Context, message string) {
	core.HandleErrorWithStatus(c, http.StatusNotFound, core.NewError(core.ErrNotFound, message))
}
