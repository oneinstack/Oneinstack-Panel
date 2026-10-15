package artifactcleanup

import (
	"net/http"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	cleanup "oneinstack/internal/services/artifactcleanup"

	"github.com/gin-gonic/gin"
)

func Inventory(c *gin.Context) {
	result, err := cleanup.List(time.Now())
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取存储清理清单失败"))
		return
	}
	manager, err := cleanup.Default(app.DB())
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "存储清理服务不可用"))
		return
	}
	busy, err := manager.Busy()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取活动任务失败"))
		return
	}
	c.JSON(http.StatusOK, core.SuccessResponseForContext(c, gin.H{"inventory": result, "taskBusy": busy}))
}

func Tasks(c *gin.Context) {
	manager, err := cleanup.Default(app.DB())
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "存储清理服务不可用"))
		return
	}
	tasks, err := manager.ListTasks(30)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取清理任务失败"))
		return
	}
	core.HandleSuccess(c, tasks)
}
