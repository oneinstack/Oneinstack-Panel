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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
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
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "防火墙参数无效"))
	case errors.Is(err, safeservice.ErrProtected):
		core.HandleError(c, core.WrapError(err, core.ErrForbidden, "系统保护规则不可修改"))
	case errors.Is(err, safeservice.ErrUnsupported):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "当前防火墙不支持此操作"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "防火墙规则不存在"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "防火墙操作失败"))
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
