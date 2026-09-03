package system

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	systemservice "oneinstack/internal/services/system"

	"github.com/gin-gonic/gin"
)

func ListProcesses(c *gin.Context) {
	offset, limit := 0, 50
	if value := c.Query("offset"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "offset 必须是大于等于 0 的整数", "offset"))
			return
		}
		offset = parsed
	}
	if value := c.Query("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "limit 必须是 1 到 200 之间的整数", "limit"))
			return
		}
		limit = parsed
	}
	sortBy := strings.ToLower(strings.TrimSpace(c.Query("sort")))
	switch sortBy {
	case "", "pid", "cpu", "memory", "name":
	default:
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "sort 仅支持 pid、cpu、memory 或 name", "sort"))
		return
	}
	order := strings.ToLower(strings.TrimSpace(c.Query("order")))
	switch order {
	case "", "asc", "desc":
	default:
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "order 仅支持 asc 或 desc", "order"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.ListProcesses(ctx, offset, limit, c.Query("keyword"), sortBy, order == "desc")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			core.HandleError(c, core.WrapError(err, core.ErrTaskTimeout, "获取进程列表超时"))
			return
		}
		core.HandleError(c, core.WrapError(err, core.ErrSystemError, "获取进程列表失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetProcessDetail(c *gin.Context) {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 32)
	if err != nil || pid < 1 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "pid 必须是正整数", "pid"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.GetProcessDetail(ctx, int32(pid))
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			core.HandleError(c, core.WrapError(err, core.ErrTaskTimeout, "获取进程详情超时"))
		case errors.Is(err, systemservice.ErrProcessNotAvailable):
			core.HandleError(c, core.NewErrorWithDetail(core.ErrNotFound, "进程不存在或已退出", "该进程可能已结束，请刷新进程列表后重试。"))
		default:
			core.HandleError(c, core.WrapError(err, core.ErrSystemError, "获取进程详情失败"))
		}
		return
	}
	core.HandleSuccess(c, result)
}

func GetDiskInventory(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "page 必须是大于等于 1 的整数", "page"))
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "pageSize 必须是 1 到 100 之间的整数", "pageSize"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.GetDiskInventory(ctx, page, pageSize)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrSystemError, "获取磁盘信息失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetSSHConfig(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	core.HandleSuccess(c, systemservice.GetSSHConfig(ctx))
}
