package safe

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"oneinstack/core"
	"oneinstack/internal/models"
	configsnapshot "oneinstack/internal/services/configsnapshot"
	safeservice "oneinstack/internal/services/safe"
	configsnapshotHandler "oneinstack/router/handler/configsnapshot"
	softwarehandler "oneinstack/router/handler/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
)

func GetFirewallInfo(c *gin.Context) {
	info, err := safeservice.NewDefaultService().Status(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"info": info})
}

func GetFirewallRules(c *gin.Context) {
	var param input.IptablesRuleParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙请求参数格式不正确"))
		return
	}
	rules, err := safeservice.NewDefaultService().List(&param)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, rules)
}

func AddFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙规则创建参数格式不正确"))
		return
	}
	service := safeservice.NewDefaultService()
	snapshot, err := beginFirewallSnapshot(c, service, "update")
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if err := service.Add(c.Request.Context(), &param); err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		handleServiceError(c, err)
		return
	}
	after, _ := service.ExportRules("")
	_ = configsnapshot.Default().MarkWithAfter(snapshot.ID, after, "succeeded", "")
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "防火墙规则已新增")
	core.HandleSuccess(c, param)
}

func UpdateFirewallRule(c *gin.Context) {
	var param models.IptablesRule
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙规则更新参数格式不正确"))
		return
	}
	service := safeservice.NewDefaultService()
	snapshot, err := beginFirewallSnapshot(c, service, "update")
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if err := service.Update(c.Request.Context(), &param); err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		handleServiceError(c, err)
		return
	}
	after, _ := service.ExportRules("")
	_ = configsnapshot.Default().MarkWithAfter(snapshot.ID, after, "succeeded", "")
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "防火墙规则已更新")
	core.HandleSuccess(c, param)
}

func DeleteFirewallRule(c *gin.Context) {
	var param struct {
		ID int64 `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙规则删除参数格式不正确"))
		return
	}
	service := safeservice.NewDefaultService()
	snapshot, err := beginFirewallSnapshot(c, service, "delete")
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if err := service.Delete(c.Request.Context(), param.ID); err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		handleServiceError(c, err)
		return
	}
	after, _ := service.ExportRules("")
	_ = configsnapshot.Default().MarkWithAfter(snapshot.ID, after, "succeeded", "")
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "防火墙规则已删除")
	core.HandleSuccess(c, nil)
}

func SetFirewallRuleState(c *gin.Context) {
	var param input.FirewallRuleStateParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙规则状态参数格式不正确"))
		return
	}
	service := safeservice.NewDefaultService()
	snapshot, err := beginFirewallSnapshot(c, service, "update")
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if err := service.SetRuleState(c.Request.Context(), param.ID, param.Enabled); err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		handleServiceError(c, err)
		return
	}
	after, _ := service.ExportRules("")
	_ = configsnapshot.Default().MarkWithAfter(snapshot.ID, after, "succeeded", "")
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "防火墙规则状态已更新")
	core.HandleSuccess(c, nil)
}

func BatchFirewallRules(c *gin.Context) {
	var param input.FirewallRuleBatchParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙批量操作参数格式不正确"))
		return
	}
	service := safeservice.NewDefaultService()
	snapshot, err := beginFirewallSnapshot(c, service, "update")
	if err != nil {
		handleServiceError(c, err)
		return
	}
	completed, err := service.Batch(c.Request.Context(), param.IDs, param.Action)
	if err != nil {
		_ = configsnapshot.Default().Mark(snapshot.ID, "failed", err.Error())
		configsnapshotHandler.RecordAudit(c, snapshot, "failed", err.Error())
		handleServiceError(c, err)
		return
	}
	after, _ := service.ExportRules("")
	_ = configsnapshot.Default().MarkWithAfter(snapshot.ID, after, "succeeded", "")
	configsnapshotHandler.RecordAudit(c, snapshot, "succeeded", "防火墙规则批量操作已完成")
	core.HandleSuccess(c, gin.H{"completed": completed})
}

func CleanupFirewallRules(c *gin.Context) {
	cleaned, err := safeservice.NewDefaultService().CleanupExpired(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"cleaned": cleaned})
}

func ExportFirewallRules(c *gin.Context) {
	rules, err := safeservice.NewDefaultService().ExportRules(c.Query("ruleType"))
	if err != nil {
		handleServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="oneinstack-firewall-rules.json"`)
	c.JSON(http.StatusOK, gin.H{
		"version": 1, "exportedAt": time.Now().UTC().Format(time.RFC3339), "rules": rules,
	})
}

func ImportFirewallRules(c *gin.Context) {
	var param input.FirewallRuleImportParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "导入文件格式错误"))
		return
	}
	rules := make([]models.IptablesRule, 0, len(param.Rules))
	for index, item := range param.Rules {
		var expiresAt *time.Time
		if item.ExpiresAt != nil && strings.TrimSpace(*item.ExpiresAt) != "" {
			value, err := time.Parse(time.RFC3339, strings.TrimSpace(*item.ExpiresAt))
			if err != nil {
				core.HandleError(c, core.NewError(
					core.ErrBadRequest,
					"第 "+strconv.Itoa(index+1)+" 条规则的过期时间格式错误",
				))
				return
			}
			expiresAt = &value
		}
		rules = append(rules, models.IptablesRule{
			RuleType: item.RuleType, Direction: item.Direction, Protocol: item.Protocol,
			Strategy: item.Strategy, IPs: item.IPs, Ports: item.Ports, State: item.State,
			Remark: item.Remark, Location: item.Location, ExpiresAt: expiresAt,
		})
	}
	imported, err := safeservice.NewDefaultService().ImportRules(c.Request.Context(), rules)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"imported": imported})
}

func ListPortForwards(c *gin.Context) {
	var param input.FirewallPortForwardParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "端口转发查询参数格式不正确"))
		return
	}
	result, err := safeservice.NewDefaultService().ListPortForwards(&param)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func AddPortForward(c *gin.Context) {
	var param models.FirewallPortForward
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "端口转发创建参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().AddPortForward(c.Request.Context(), &param); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, param)
}

func UpdatePortForward(c *gin.Context) {
	var param models.FirewallPortForward
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "端口转发更新参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().UpdatePortForward(c.Request.Context(), &param); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, param)
}

func DeletePortForward(c *gin.Context) {
	var param struct {
		ID int64 `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "端口转发删除参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().DeletePortForward(c.Request.Context(), param.ID); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func SetPortForwardState(c *gin.Context) {
	var param input.FirewallPortForwardStateParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "端口转发状态参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().SetPortForwardState(c.Request.Context(), param.ID, param.Enabled); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func GetAutoBlockConfig(c *gin.Context) {
	config, err := safeservice.NewDefaultService().GetAutoBlockConfig()
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"config": config})
}

func SaveAutoBlockConfig(c *gin.Context) {
	var param input.FirewallAutoBlockParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "自动封禁配置参数格式不正确"))
		return
	}
	config, err := safeservice.NewDefaultService().SaveAutoBlockConfig(&param)
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"config": config})
}

func RunAutoBlock(c *gin.Context) {
	blocked, err := safeservice.NewDefaultService().RunAutoBlock(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"blocked": blocked})
}

// StopFirewall 保留历史路由名称，实际按 enabled 字段设置目标状态，不再执行不确定的 toggle。
func StopFirewall(c *gin.Context) {
	var param input.FirewallToggleParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙停止参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().SetEnabled(
		c.Request.Context(), param.Enabled, param.Confirm,
	); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func BlockPing(c *gin.Context) {
	var param input.FirewallPingParam
	if err := c.ShouldBindJSON(&param); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙 Ping 设置参数格式不正确"))
		return
	}
	if err := safeservice.NewDefaultService().SetPingBlocked(c.Request.Context(), param.Blocked); err != nil {
		handleServiceError(c, err)
		return
	}
	core.HandleSuccess(c, nil)
}

func InstallFirewall(c *gin.Context) {
	status, err := safeservice.NewDefaultService().Status(c.Request.Context())
	if err != nil {
		handleServiceError(c, err)
		return
	}
	if status.Install && !(status.Backend == safeservice.BackendFirewalld && status.RepairRequired) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "服务器已安装受支持的防火墙"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	task, err := softwarehandler.SubmitInstallationTask(input.InstallParams{
		Key:     "firewalld",
		Version: "1.0.0",
	}, userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建 firewalld 安装任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
		"taskId":      task.ID,
		"installName": task.ID,
		"operation":   task.Operation,
		"component":   task.Component,
		"status":      task.Status,
		"progress":    task.Progress,
		"statusUrl":   "/v1/soft/tasks/" + task.ID,
		"streamUrl":   "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

func handleServiceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, safeservice.ErrValidation):
		message := safeservice.ValidationMessage(err)
		if message == "" {
			message = "防火墙参数无效"
		}
		core.HandleError(c, core.NewError(core.ErrBadRequest, message))
	case errors.Is(err, safeservice.ErrAutoBlockDisabled):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrResourceStateInvalid, "自动封禁未启用，不能立即检测"))
	case errors.Is(err, safeservice.ErrProtected):
		core.HandleError(c, core.WrapError(err, core.ErrForbidden, "系统保护规则不可修改"))
	case errors.Is(err, safeservice.ErrUnsupported):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "当前防火墙不支持此操作"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "防火墙规则不存在"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, firewallOperationMessage(c)))
	}
}

func firewallOperationMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/safe/info":
		return "读取防火墙状态失败"
	case "/v1/safe/rules":
		return "读取防火墙规则列表失败"
	case "/v1/safe/add":
		return "创建防火墙规则失败"
	case "/v1/safe/update":
		return "更新防火墙规则失败"
	case "/v1/safe/del":
		return "删除防火墙规则失败"
	case "/v1/safe/rules/state":
		return "更新防火墙规则状态失败"
	case "/v1/safe/rules/batch":
		return "执行防火墙规则批量操作失败"
	case "/v1/safe/rules/cleanup":
		return "清理防火墙规则失败"
	case "/v1/safe/rules/export":
		return "导出防火墙规则失败"
	case "/v1/safe/rules/import":
		return "导入防火墙规则失败"
	case "/v1/safe/forwards":
		return "读取端口转发列表失败"
	case "/v1/safe/forwards/add":
		return "创建端口转发失败"
	case "/v1/safe/forwards/update":
		return "更新端口转发失败"
	case "/v1/safe/forwards/del":
		return "删除端口转发失败"
	case "/v1/safe/forwards/state":
		return "更新端口转发状态失败"
	case "/v1/safe/auto-block":
		if c.Request.Method == http.MethodGet {
			return "读取自动封禁配置失败"
		}
		return "保存自动封禁配置失败"
	case "/v1/safe/auto-block/run":
		return "执行自动封禁任务失败"
	case "/v1/safe/stop":
		return "停止防火墙服务失败"
	case "/v1/safe/blockping":
		return "更新防火墙 Ping 响应设置失败"
	case "/v1/safe/install":
		return "安装防火墙失败"
	default:
		return "处理防火墙请求失败"
	}
}

func beginFirewallSnapshot(c *gin.Context, service *safeservice.Service, operation string) (*models.ConfigurationSnapshot, error) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		return nil, core.NewError(core.ErrUnauthorized, "登录状态无效")
	}
	before, err := service.ExportRules("")
	if err != nil {
		return nil, fmt.Errorf("读取当前防火墙规则: %w", err)
	}
	return configsnapshot.Default().Create(configsnapshot.CreateInput{
		ResourceType: "firewall", ResourceID: "host", Operation: operation,
		Before: before, After: before, RequestedBy: userID,
	})
}
