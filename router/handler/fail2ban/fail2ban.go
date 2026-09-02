package fail2ban

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	fail2banservice "oneinstack/internal/services/fail2ban"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func Status(c *gin.Context) {
	result, err := fail2banservice.DefaultService().Status(c.Request.Context())
	if result != nil {
		result.Warning = i18n.LocalizeBusinessText(c.GetString("locale"), result.Warning)
	}
	respond(c, result, err)
}

func Templates(c *gin.Context) {
	result := fail2banservice.Templates()
	for i := range result {
		result[i].Name = i18n.LocalizeBusinessText(c.GetString("locale"), result[i].Name)
		result[i].Description = i18n.LocalizeBusinessText(c.GetString("locale"), result[i].Description)
	}
	core.HandleSuccess(c, result)
}

func Policies(c *gin.Context) {
	result, err := fail2banservice.DefaultService().ListPolicies(c.Request.Context())
	if err == nil {
		locale := middleware.RequestLocale(c)
		for index := range result {
			result[index].Name = i18n.LocalizeBusinessText(locale, result[index].Name)
		}
	}
	respond(c, result, err)
}

func Bans(c *gin.Context) {
	result, err := fail2banservice.DefaultService().ListBans(c.Request.Context())
	if err == nil {
		locale := middleware.RequestLocale(c)
		for index := range result {
			result[index].Policy = i18n.LocalizeBusinessText(locale, result[index].Policy)
		}
	}
	respond(c, result, err)
}

func Incidents(c *gin.Context) {
	page, pageSize := pagination(c)
	query := app.DB().Model(&models.SecurityIncident{})
	if value := strings.TrimSpace(c.Query("status")); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(c.Query("remoteIp")); value != "" {
		query = query.Where("remote_ip = ?", value)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		respond(c, nil, err)
		return
	}
	var incidents []models.SecurityIncident
	if err := query.Order("last_seen_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&incidents).Error; err != nil {
		respond(c, nil, err)
		return
	}
	access, _ := middleware.UserAccess(c)
	if access == nil || !access.HasPermission(accessservice.PermissionAuditRead) {
		for i := range incidents {
			incidents[i].Evidence = nil
		}
		core.HandleSuccess(c, gin.H{"data": incidents, "total": total, "page": page, "pageSize": pageSize})
		return
	}
	type incidentWithAudit struct {
		models.SecurityIncident
		AuditEvidence []models.AuditEvent `json:"auditEvidence,omitempty"`
	}
	result := make([]incidentWithAudit, 0, len(incidents))
	for _, incident := range incidents {
		view := incidentWithAudit{SecurityIncident: incident}
		if len(incident.Evidence) > 0 {
			_ = app.DB().Select("id", "sequence", "action", "outcome", "remote_ip", "message", "created_at").
				Where("id IN ?", incident.Evidence).Order("sequence ASC").Find(&view.AuditEvidence).Error
		}
		result = append(result, view)
	}
	core.HandleSuccess(c, gin.H{"data": result, "total": total, "page": page, "pageSize": pageSize})
}

func DismissIncident(c *gin.Context) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	respond(c, gin.H{"id": c.Param("id"), "status": "dismissed"}, fail2banservice.DefaultService().DismissIncident(c.Param("id"), userID))
}

func Tasks(c *gin.Context) {
	page, pageSize := pagination(c)
	result, err := fail2banservice.DefaultManager().List(fail2banservice.TaskListOptions{
		ActiveOnly: c.Query("active") == "true", Operation: c.Query("operation"), Page: page, PageSize: pageSize,
	})
	respond(c, result, err)
}

func UnbanRecords(c *gin.Context) {
	page, pageSize := pagination(c)
	result, err := fail2banservice.DefaultManager().List(fail2banservice.TaskListOptions{
		Operation: "unban_ip", Page: page, PageSize: pageSize,
	})
	respond(c, result, err)
}

func Task(c *gin.Context) {
	result, err := fail2banservice.DefaultManager().Get(c.Param("id"))
	respond(c, result, err)
}

func TaskEvents(c *gin.Context) {
	after := parseInt64(c.GetHeader("Last-Event-ID"))
	if queryAfter := parseInt64(c.Query("after")); queryAfter > after {
		after = queryAfter
	}
	if !strings.Contains(c.GetHeader("Accept"), "text/event-stream") {
		result, err := fail2banservice.DefaultManager().EventsAfter(c.Param("id"), after)
		respond(c, result, err)
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrServiceUnavailable, "当前服务不支持任务事件流"))
		return
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		events, err := fail2banservice.DefaultManager().EventsAfter(c.Param("id"), after)
		if err != nil {
			return
		}
		for _, event := range events {
			payload, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(c.Writer, "id: %d\nevent: task\ndata: %s\n\n", event.Seq, payload)
			after = event.Seq
		}
		flusher.Flush()
		task, err := fail2banservice.DefaultManager().Get(c.Param("id"))
		if err != nil || (models.IsFail2banTaskTerminal(task.Status) && after >= task.EventSeq) {
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-ticker.C:
		}
	}
}

func TaskLog(c *gin.Context) {
	after := parseInt64(c.Query("cursor"))
	events, err := fail2banservice.DefaultManager().EventsAfter(c.Param("id"), after)
	if err != nil {
		respond(c, nil, err)
		return
	}
	var builder strings.Builder
	next := after
	for _, event := range events {
		fmt.Fprintf(&builder, "%s [%s] %s\n", event.CreatedAt.Format(time.RFC3339), event.Level, event.Message)
		next = event.Seq
	}
	task, _ := fail2banservice.DefaultManager().Get(c.Param("id"))
	eof := task != nil && models.IsFail2banTaskTerminal(task.Status) && next >= task.EventSeq
	core.HandleSuccess(c, gin.H{"content": builder.String(), "nextCursor": next, "eof": eof})
}

func respond(c *gin.Context, data any, err error) {
	if err == nil {
		core.HandleSuccess(c, data)
		return
	}
	code, message := core.ErrInternalError, "读取入侵防护数据失败"
	switch {
	case errors.Is(err, fail2banservice.ErrValidation):
		code, message = core.ErrValidationFailed, "入侵防护参数无效，请检查后重试"
	case errors.Is(err, fail2banservice.ErrRevisionConflict):
		code, message = core.ErrResourceStateInvalid, "规则已被其他操作修改，请刷新后重试"
	case errors.Is(err, fail2banservice.ErrProtectedAddress):
		code, message = core.ErrInsufficientPermissions, "该地址属于系统保护范围，不能封禁"
	case errors.Is(err, fail2banservice.ErrUnavailable):
		code, message = core.ErrServiceUnavailable, "Fail2ban 未安装、未验证或服务不可用"
	case errors.Is(err, gorm.ErrRecordNotFound):
		code, message = core.ErrNotFound, "目标入侵防护资源不存在"
	}
	detail := err.Error()
	if errors.Is(err, fail2banservice.ErrValidation) {
		detail = strings.TrimSpace(strings.TrimPrefix(detail, fail2banservice.ErrValidation.Error()+":"))
		if detail == "" {
			detail = "请检查策略动作、模板和参数取值后重试。"
		}
	}
	core.HandleError(c, core.NewErrorWithDetail(code, message, detail))
}

func pagination(c *gin.Context) (int, int) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	return page, pageSize
}

func parseInt64(value string) int64 {
	result, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if result < 0 {
		return 0
	}
	return result
}
