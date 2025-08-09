package log

import (
	"oneinstack/core"
	logService "oneinstack/internal/services/log"

	"github.com/gin-gonic/gin"
)

// WebSocketLogHandler 处理WebSocket日志连接
func WebSocketLogHandler(c *gin.Context) {
	logManager := logService.GetLogManager()
	logManager.HandleWebSocketConnection(c)
}

// GetLogHistoryHandler 获取历史日志
func GetLogHistoryHandler(c *gin.Context) {
	logManager := logService.GetLogManager()
	logManager.GetLogHistory(c)
}

// GetLogStatusHandler 获取日志状态
func GetLogStatusHandler(c *gin.Context) {
	taskID := c.Query("task_id")
	if taskID == "" {
		appErr := core.NewError(core.ErrBadRequest, "task_id参数必需")
		core.HandleError(c, appErr)
		return
	}

	logManager := logService.GetLogManager()
	stream, exists := logManager.GetLogStream(taskID)
	if !exists {
		appErr := core.NewError(core.ErrNotFound, "任务不存在")
		core.HandleError(c, appErr)
		return
	}

	core.HandleSuccess(c, gin.H{
		"task_id":   taskID,
		"completed": stream.IsCompleted,
		"progress":  stream.Progress,
	})
}
