package ftp

import (
	"context"
	"time"

	"oneinstack/core"
	"oneinstack/internal/services/filemanager"

	"github.com/gin-gonic/gin"
)

const fileSearchTimeout = 12 * time.Second

var fileSearchSlots = make(chan struct{}, 2)

func SearchFile(c *gin.Context) {
	var input struct {
		Path       string `json:"path" binding:"required"`
		Query      string `json:"query" binding:"required"`
		Type       string `json:"type"`
		MaxResults int    `json:"maxResults"`
		MaxDepth   int    `json:"maxDepth"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "搜索参数错误")
		return
	}
	select {
	case fileSearchSlots <- struct{}{}:
		defer func() { <-fileSearchSlots }()
	default:
		core.HandleError(c, core.NewError(core.ErrRateLimitExceeded, "当前文件搜索任务较多，请稍后重试"))
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	searchContext, cancel := context.WithTimeout(c.Request.Context(), fileSearchTimeout)
	defer cancel()
	result, err := manager.Search(searchContext, filemanager.SearchOptions{
		Path:       input.Path,
		Query:      input.Query,
		Type:       input.Type,
		MaxResults: input.MaxResults,
		MaxDepth:   input.MaxDepth,
	})
	if err != nil {
		handleFileError(c, err, "搜索文件失败")
		return
	}
	core.HandleSuccess(c, result)
}
