package cluster

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"oneinstack/core"
	clusterservice "oneinstack/internal/services/cluster"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ReportHealth(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 512<<10)
	var report clusterservice.HealthReport
	if err := c.ShouldBindJSON(&report); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "集群健康报告格式无效或过大"))
		return
	}
	report.Token = agentToken(c, report.Token)
	if err := m.ReportHealth(c.Request.Context(), report); err != nil {
		if errors.Is(err, clusterservice.ErrInvalidToken) || errors.Is(err, clusterservice.ErrNodeDisabled) {
			core.HandleErrorWithStatus(c, http.StatusUnauthorized, core.NewError(core.ErrUnauthorized, "节点令牌无效或节点已停用"))
			return
		}
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "集群健康报告未被接受"))
		return
	}
	core.HandleSuccess(c, gin.H{"accepted": true})
}

func HealthSummary(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	result, err := m.HealthSummary()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取集群健康汇总失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func HealthResources(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var nodeID uint64
	var err error
	if raw := strings.TrimSpace(c.Query("nodeId")); raw != "" {
		nodeID, err = strconv.ParseUint(raw, 10, 32)
		if err != nil || nodeID == 0 {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点 ID 无效"))
			return
		}
	}
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, sizeErr := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	kind := strings.TrimSpace(c.Query("resourceType"))
	status := strings.TrimSpace(c.Query("status"))
	validTypes := map[string]bool{"": true, "node": true, "task": true, "website": true, "certificate": true, "database": true, "service": true, "backup": true}
	validStatuses := map[string]bool{"": true, "attention": true, "healthy": true, "warning": true, "critical": true, "unknown": true, "unprotected": true, "disabled": true}
	if pageErr != nil || sizeErr != nil || page < 1 || pageSize < 1 || pageSize > 100 || !validTypes[kind] || !validStatuses[status] {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "健康资源筛选或分页参数无效"))
		return
	}
	result, err := m.ListHealthResources(uint(nodeID), kind, status, page, pageSize)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取集群健康资源失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func HealthEvents(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	page, pageErr := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, sizeErr := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if pageErr != nil || sizeErr != nil || page < 1 || pageSize < 1 || pageSize > 100 {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "健康事件分页参数无效"))
		return
	}
	result, err := m.ListHealthEvents(page, pageSize)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取集群健康事件失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func HealthResourceDetail(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		core.HandleError(c, core.NewError(core.ErrInvalidID, "健康资源 ID 无效"))
		return
	}
	row, err := m.GetHealthResource(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.HandleError(c, core.NewError(core.ErrNotFound, "健康资源不存在"))
		} else {
			core.HandleError(c, core.NewError(core.ErrInternalError, "读取健康资源详情失败"))
		}
		return
	}
	core.HandleSuccess(c, row)
}

func UpdateHealthNotification(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		core.HandleError(c, core.NewError(core.ErrInvalidID, "健康资源 ID 无效"))
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.Enabled == nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "通知开关参数无效"))
		return
	}
	row, err := m.SetHealthNotification(c.Request.Context(), id, *input.Enabled)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.HandleError(c, core.NewError(core.ErrNotFound, "健康资源不存在"))
		} else {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, "无法修改该资源的通知设置"))
		}
		return
	}
	core.HandleSuccess(c, row)
}
