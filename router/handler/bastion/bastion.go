package bastion

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	bastionservice "oneinstack/internal/services/bastion"
	"oneinstack/router/middleware"

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
	writeServersResult(c, servers, err)
}

// ListServers 服务器列表
func ListServers(c *gin.Context) {
	manager := bastionservice.Default()
	if !checkEnabled(c, manager) {
		return
	}
	servers, err := manager.ListServers()
	writeServersResult(c, servers, err)
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
	writeServerResult(c, server, err)
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
	if strings.TrimSpace(input.KeyPath) != "" {
		writeBadRequest(c, errors.New("不允许指定私钥路径，请直接提交 privateKey"))
		return
	}
	server, err := manager.CreateServer(input)
	if err != nil {
		writeBadRequest(c, err)
		return
	}
	writeServerResult(c, server, nil)
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
	if strings.TrimSpace(input.KeyPath) != "" {
		writeBadRequest(c, errors.New("不允许指定私钥路径，请直接提交 privateKey"))
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
	writeServerResult(c, server, nil)
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
		Password string `json:"password"`
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
	if !result.Reachable {
		core.HandleErrorWithStatus(c, http.StatusBadGateway, bastionConnectionError(result.Error))
		return
	}
	core.HandleSuccess(c, result)
}

func bastionConnectionError(probeError string) *core.AppError {
	lower := strings.ToLower(strings.TrimSpace(probeError))
	switch {
	case strings.Contains(lower, "connection refused"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"目标服务器 SSH 端口拒绝连接",
			"目标服务器可访问，但 SSH 端口未接受连接；请确认 SSH 服务已启动，并检查端口、安全组和防火墙配置。",
		)
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "server misbehaving"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"目标服务器地址解析失败",
			"无法解析目标服务器地址，请检查主机名或 IP 配置以及面板服务器的 DNS 设置。",
		)
	case strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "no route to host"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"目标服务器网络不可达",
			"面板服务器当前没有到目标服务器的可用网络路由，请检查网络、路由、安全组和防火墙配置。",
		)
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "探测超时"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"目标服务器 SSH 连接测试超时",
			"目标服务器在限定时间内未完成连接或探测，请检查 SSH 端口、安全组、防火墙和服务器负载后重试。",
		)
	case strings.Contains(lower, "unable to authenticate"), strings.Contains(lower, "permission denied"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"SSH 身份认证失败",
			"已连接到目标 SSH 服务，但用户名、密码或私钥未通过认证，请核对登录账号和认证凭据。",
		)
	case strings.HasPrefix(lower, "ssh 配置无效"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"SSH 认证配置无效",
			"当前服务器记录缺少有效的 SSH 用户名、密码或托管私钥，请完善认证配置后重试。",
		)
	case strings.HasPrefix(lower, "ssh 会话创建失败"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"SSH 会话创建失败",
			"SSH 连接已建立，但无法创建远程会话；请检查目标账号的登录权限和 SSH 服务策略。",
		)
	case strings.HasPrefix(lower, "远程命令执行失败"):
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"服务器已连接，但系统信息探测失败",
			"SSH 会话已建立，但只读系统信息命令无法执行；请检查目标账号的命令执行权限和服务器环境。",
		)
	default:
		return core.NewErrorWithDetail(
			core.ErrInternalError,
			"目标服务器 SSH 连接测试失败",
			"连接测试未完成，请核对服务器地址、SSH 端口、认证方式和网络访问策略后重试。",
		)
	}
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
		core.HandleErrorWithStatus(c, http.StatusServiceUnavailable,
			core.NewErrorWithDetail(core.ErrServiceUnavailable, "堡垒机服务不可用，请先启用堡垒机模块", "堡垒机模块未启用或服务管理器未初始化，请先在面板配置中启用堡垒机模块。"))
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
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, bastionOperationMessage(c)))
		return
	}
	core.HandleSuccess(c, result)
}

func bastionOperationMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/bastion/overview", "/v1/bastion/servers":
		return "读取堡垒机服务器列表失败"
	case "/v1/bastion/servers/:id":
		switch c.Request.Method {
		case http.MethodPut:
			return "更新堡垒机服务器失败"
		case http.MethodDelete:
			return "删除堡垒机服务器失败"
		default:
			return "读取堡垒机服务器详情失败"
		}
	case "/v1/bastion/servers/:id/test":
		return "测试堡垒机服务器 SSH 连接失败"
	case "/v1/bastion/servers/:id/metrics":
		return "读取堡垒机服务器监控指标失败"
	default:
		return "堡垒机请求处理失败"
	}
}

func writeServersResult(c *gin.Context, servers []models.BastionServerSummary, err error) {
	if err != nil {
		writeResult(c, nil, err)
		return
	}
	if !canViewIdentity(c) {
		for i := range servers {
			servers[i].Username = "***"
		}
	}
	core.HandleSuccess(c, servers)
}

func writeServerResult(c *gin.Context, server *models.BastionServer, err error) {
	if err != nil {
		writeResult(c, nil, err)
		return
	}
	if server != nil && !canViewIdentity(c) {
		copy := *server
		copy.Username = "***"
		server = &copy
	}
	core.HandleSuccess(c, server)
}

func canViewIdentity(c *gin.Context) bool {
	access, ok := middleware.UserAccess(c)
	return ok && access.HasPermission(accessservice.PermissionBastionIdentityRead)
}

func writeBadRequest(c *gin.Context, err error) {
	core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "堡垒机请求参数格式不正确", err.Error()))
}

func writeNotFound(c *gin.Context, message string) {
	core.HandleErrorWithStatus(c, http.StatusNotFound, core.NewError(core.ErrNotFound, message))
}
