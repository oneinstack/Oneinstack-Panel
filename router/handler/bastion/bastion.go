package bastion

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	bastionservice "oneinstack/internal/services/bastion"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Overview 堡垒机总览（所有服务器及最新指标）
func Overview(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	servers, err := manager.ListServers()
	writeResult(c, servers, err)
}

// ListServers 服务器列表
func ListServers(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	servers, err := manager.ListServers()
	writeResult(c, servers, err)
}

// GetServer 服务器详情
func GetServer(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	server, err := manager.GetServer(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "服务器不存在")
		return
	}
	writeResult(c, server, err)
}

// CreateServer 添加服务器
func CreateServer(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	var input bastionservice.CreateServerInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	server, err := manager.CreateServer(input)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, server)
}

// UpdateServer 编辑服务器
func UpdateServer(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input bastionservice.UpdateServerInput
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	server, err := manager.UpdateServer(id, input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "服务器不存在")
		return
	}
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	core.HandleSuccess(c, server)
}

// DeleteServer 删除服务器
func DeleteServer(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	err := manager.DeleteServer(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeNotFound(c, "服务器不存在")
		return
	}
	writeResult(c, gin.H{"deleted": err == nil}, err)
}

// TestConnection 测试连接
func TestConnection(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		writeBadRequest(c, err)
		return
	}
	result, err := manager.TestConnection(id, input.Password)
	if err != nil {
		writeResult(c, nil, err)
		return
	}
	core.HandleSuccess(c, result)
}

// GetMetrics 获取历史指标
func GetMetrics(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	id, ok := parseID(c)
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
	limit := optionalInt(c.Query("limit"), 200)
	if limit > 2000 {
		limit = 2000
	}
	samples, err := manager.GetMetrics(id, from, to, limit)
	writeResult(c, samples, err)
}

func checkEnabled(c *gin.Context, manager *bastionservice.Manager) bool {
	if manager == nil || !app.ONE_CONFIG.Bastion.Enabled {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"success": false,
			"code":    "BASTION_UNAVAILABLE",
			"message": "堡垒机模块未启用",
		})
		return false
	}
	return true
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
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "堡垒机操作失败"))
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
