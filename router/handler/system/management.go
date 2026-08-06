package system

import (
	"context"
	"strconv"
	"time"

	"oneinstack/core"
	systemservice "oneinstack/internal/services/system"

	"github.com/gin-gonic/gin"
)

func ListProcesses(c *gin.Context) {
	offset, limit := 0, 50
	var err error
	if value := c.Query("offset"); value != "" {
		offset, err = strconv.Atoi(value)
	}
	if err == nil {
		if value := c.Query("limit"); value != "" {
			limit, err = strconv.Atoi(value)
		}
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "分页参数错误"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.ListProcesses(ctx, offset, limit, c.Query("keyword"), c.Query("sort"), c.Query("order") == "desc")
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrSystemError, "获取进程列表失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetProcessDetail(c *gin.Context) {
	pid, err := strconv.ParseInt(c.Param("pid"), 10, 32)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "进程 ID 无效"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.GetProcessDetail(ctx, int32(pid))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "获取进程详情失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetDiskInventory(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	result, err := systemservice.GetDiskInventory(ctx)
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
