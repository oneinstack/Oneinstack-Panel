package operationpreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	previewservice "oneinstack/internal/services/operationpreview"
	safeservice "oneinstack/internal/services/safe"
	softwareService "oneinstack/internal/services/software"
	systemservice "oneinstack/internal/services/system"
	"oneinstack/internal/services/website"
	"oneinstack/router/handler/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

type previewRequest struct {
	Operation string          `json:"operation" binding:"required"`
	Payload   json.RawMessage `json:"payload"`
}

type executeRequest struct {
	Confirm bool `json:"confirm"`
}

func Preview(c *gin.Context) {
	var request previewRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "预览请求格式错误"))
		return
	}
	operation := strings.TrimSpace(request.Operation)
	if !supportedOperation(operation) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "不支持的操作类型"))
		return
	}
	if err := requireOperationPermission(c, operation); err != nil {
		core.HandleError(c, err)
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	document, resourceVersion, err := buildDocument(operation, request.Payload)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "生成操作预览失败"))
		return
	}
	service := previewservice.New(app.DB())
	created, err := service.Create(operation, userID, request.Payload, document, resourceVersion)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "保存操作预览失败"))
		return
	}
	core.HandleSuccess(c, created)
}

func Execute(c *gin.Context) {
	var request executeRequest
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "必须确认操作预览后才能执行"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	previewService := previewservice.New(app.DB())
	operation, err := previewService.Peek(c.Param("previewId"), userID)
	if err != nil {
		writeConsumeError(c, err)
		return
	}
	if err := requireOperationPermission(c, operation); err != nil {
		core.HandleError(c, err)
		return
	}
	operation, payload, _, err := previewService.Consume(c.Param("previewId"), userID)
	if err != nil {
		writeConsumeError(c, err)
		return
	}
	result, err := executeOperation(c.Request.Context(), operation, payload, userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrConfigError, "执行已确认的操作预览失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, result))
}

func supportedOperation(operation string) bool {
	_, ok := accessservice.OperationPermission(operation)
	return ok
}

func requireOperationPermission(c *gin.Context, operation string) *core.AppError {
	access, exists := c.Get(middleware.ContextUserAccess)
	if !exists {
		return core.NewError(core.ErrForbidden, "权限上下文不可用")
	}
	userAccess, ok := access.(*accessservice.UserAccess)
	if !ok || userAccess == nil {
		return core.NewError(core.ErrForbidden, "权限上下文无效")
	}
	permission, supported := accessservice.OperationPermission(operation)
	if !supported || !userAccess.HasPermission(permission) {
		return core.NewError(core.ErrForbidden, "没有执行该操作的权限")
	}
	return nil
}

func buildDocument(operation string, payload json.RawMessage) (previewservice.Document, string, error) {
	if _, _, err := previewservice.NormalizePayload(payload); err != nil {
		return previewservice.Document{}, "", err
	}
	document := previewservice.Document{
		Review:    previewservice.Review{Required: true, RiskLevel: "high", Reason: "该操作会改变系统状态，执行前需要确认"},
		Prechecks: []previewservice.Precheck{{Name: "实际系统状态", Status: "deferred", Message: "执行阶段将重新探测并校验"}},
		Rollback:  previewservice.Rollback{Supported: true, Summary: "执行失败时由对应业务事务或任务流程尝试恢复"},
	}
	switch operation {
	case "website.create", "website.update":
		document.Files = []previewservice.FileChange{{Path: "受管 Nginx 虚拟主机目录/<name>.conf", Action: "create_or_update", ChangeSummary: "写入网站虚拟主机配置"}}
		document.Actions = []previewservice.Action{{Type: "command", Name: "校验 Nginx 配置", DisplayCommand: "nginx -t"}, {Type: "service", Name: "重新加载 Nginx", DisplayCommand: "nginx -s reload", Service: "nginx"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
	case "website.toggle":
		document.Files = []previewservice.FileChange{{Path: "受管 Nginx 虚拟主机目录/<name>.conf", Action: "enable_or_remove", ChangeSummary: "启用或停用网站虚拟主机配置"}}
		document.Actions = []previewservice.Action{{Type: "service", Name: "重新加载 Nginx", DisplayCommand: "nginx -s reload", Service: "nginx"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
	case "software.install":
		document.Actions = []previewservice.Action{{Type: "component", Name: "执行受控软件安装动作", DisplayCommand: "由组件安装器按软件 key 和版本执行"}, {Type: "service", Name: "安装后验证服务状态", Service: "由组件探测器确定"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "任务失败时由软件任务执行器按组件策略回滚或保留失败现场"}
	case "software.uninstall":
		document.Actions = []previewservice.Action{{Type: "component", Name: "执行受控软件卸载动作", DisplayCommand: "由组件卸载器按软件 key 和版本执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true}
	case "software.service_action":
		document.Actions = []previewservice.Action{{Type: "service", Name: "执行组件服务动作", DisplayCommand: "systemctl <action> <component>", Service: "由组件参数确定"}}
		document.Impact = previewservice.Impact{RestartService: true}
	case "software.configure":
		document.Files = []previewservice.FileChange{{Path: "组件受管配置文件", Action: "update", ChangeSummary: "应用组件配置并保存历史"}}
		document.Actions = []previewservice.Action{{Type: "command", Name: "校验并应用组件配置", DisplayCommand: "由组件配置动作执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, ReloadService: true}
	case "firewall.rule_change":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改防火墙规则", DisplayCommand: "由检测到的防火墙后端执行受控规则动作"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "失败时恢复已应用的规则操作和持久化状态"}
	case "firewall.port_forward":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "修改端口转发", DisplayCommand: "由检测到的防火墙后端执行受控转发动作"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
	case "firewall.toggle":
		document.Actions = []previewservice.Action{{Type: "firewall", Name: "切换防火墙状态", DisplayCommand: "由检测到的防火墙后端执行启停动作"}}
		document.Impact = previewservice.Impact{ModifyDatabase: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: false, Summary: "防火墙启停可能导致当前连接中断，请确认后执行", Unrecoverable: []string{"已断开的外部连接"}}
	case "panel.network":
		document.Files = []previewservice.FileChange{{Path: "面板受管配置文件", Action: "update", ChangeSummary: "更新面板监听、TLS、安全入口和可信代理配置"}}
		document.Actions = []previewservice.Action{{Type: "system", Name: "应用面板访问配置", DisplayCommand: "由面板网络配置事务执行"}, {Type: "firewall", Name: "同步面板端口规则", DisplayCommand: "由受控防火墙适配器执行"}}
		document.Impact = previewservice.Impact{WriteFiles: true, ModifyDatabase: true, RestartService: true, NetworkRisk: true}
		document.Rollback = previewservice.Rollback{Supported: true, Summary: "由面板网络事务恢复配置文件和已准备的端口规则"}
	}
	return document, "", nil
}

func executeOperation(ctx context.Context, operation string, payload json.RawMessage, userID int64) (gin.H, error) {
	switch operation {
	case "website.create":
		var value models.Website
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		if err := service.Add(ctx, &value); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "resourceId": value.ID, "status": "succeeded"}, nil
	case "website.update":
		var value models.Website
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		if err := service.Update(ctx, &value); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "resourceId": value.ID, "status": "succeeded"}, nil
	case "website.toggle":
		var value input.WebsiteStatusParam
		var raw struct {
			ID      int64 `json:"id"`
			Enabled bool  `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &raw); err != nil {
			return nil, err
		}
		value.Enabled = raw.Enabled
		service, err := website.DefaultService()
		if err != nil {
			return nil, err
		}
		site, err := service.SetEnabled(ctx, raw.ID, value.Enabled)
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "resourceId": site.ID, "status": "succeeded"}, nil
	case "software.install":
		var value input.InstallParams
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		task, err := software.SubmitInstallationTask(value, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.uninstall":
		var value input.RemoveParams
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitUninstall(value.Name, value.Version, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.service_action":
		var value struct {
			Component string `json:"component"`
			Action    string `json:"action"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitServiceAction(value.Component, value.Action, userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "software.configure":
		var value struct {
			Component string            `json:"component"`
			Revision  string            `json:"revision"`
			Values    map[string]string `json:"values"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		definition, err := softwareService.NormalizeServiceComponent(value.Component)
		if err != nil {
			return nil, err
		}
		var installed models.Software
		if err := app.DB().Where("`key` = ? AND installed = ?", definition.SoftwareKey, true).Order("install_time DESC").First(&installed).Error; err != nil {
			return nil, err
		}
		version := strings.TrimSpace(installed.InstallVersion)
		if version == "" {
			version = strings.TrimSpace(installed.Version)
		}
		if version == "" {
			return nil, errors.New("component install version is missing")
		}
		current, err := softwareService.NewInstaller().InspectServiceConfiguration(ctx, definition.Component, version)
		if err != nil {
			return nil, err
		}
		manager, err := software.DefaultTaskManager()
		if err != nil {
			return nil, err
		}
		task, err := manager.SubmitConfiguration(definition.Component, value.Revision, current.Values, value.Values, "", userID)
		if err != nil {
			return nil, err
		}
		return softwareTaskResult(task), nil
	case "firewall.rule_change":
		var value struct {
			Action  string              `json:"action"`
			Rule    models.IptablesRule `json:"rule"`
			ID      int64               `json:"id"`
			Enabled *bool               `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service := safeservice.NewDefaultService()
		var err error
		switch strings.ToLower(value.Action) {
		case "add":
			err = service.Add(ctx, &value.Rule)
		case "update":
			err = service.Update(ctx, &value.Rule)
		case "delete":
			err = service.Delete(ctx, value.ID)
		case "state":
			if value.Enabled == nil {
				return nil, errors.New("enabled is required")
			}
			err = service.SetRuleState(ctx, value.ID, *value.Enabled)
		default:
			return nil, errors.New("unsupported firewall rule action")
		}
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "firewall.port_forward":
		var value struct {
			Action  string                     `json:"action"`
			Forward models.FirewallPortForward `json:"forward"`
			ID      int64                      `json:"id"`
			Enabled *bool                      `json:"enabled"`
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		service := safeservice.NewDefaultService()
		var err error
		switch strings.ToLower(value.Action) {
		case "add":
			err = service.AddPortForward(ctx, &value.Forward)
		case "update":
			err = service.UpdatePortForward(ctx, &value.Forward)
		case "delete":
			err = service.DeletePortForward(ctx, value.ID)
		case "state":
			if value.Enabled == nil {
				return nil, errors.New("enabled is required")
			}
			err = service.SetPortForwardState(ctx, value.ID, *value.Enabled)
		default:
			return nil, errors.New("unsupported port forward action")
		}
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "firewall.toggle":
		var value input.FirewallToggleParam
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		if err := safeservice.NewDefaultService().SetEnabled(ctx, value.Enabled, value.Confirm); err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "status": "succeeded"}, nil
	case "panel.network":
		var value systemservice.UpdatePanelNetworkRequest
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		settings, err := systemservice.UpdatePanelNetwork(value)
		if err != nil {
			return nil, err
		}
		return gin.H{"operation": operation, "settings": settings, "status": "succeeded"}, nil
	default:
		return nil, fmt.Errorf("unsupported operation %s", operation)
	}
}

func softwareTaskResult(task *models.SoftwareTask) gin.H {
	return gin.H{"taskId": task.ID, "operation": task.Operation, "component": task.Component, "status": task.Status, "statusUrl": "/v1/soft/tasks/" + task.ID, "streamUrl": "/v1/soft/tasks/" + task.ID + "/events"}
}

func writeConsumeError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, previewservice.ErrNotFound), errors.Is(err, previewservice.ErrExpired), errors.Is(err, previewservice.ErrConsumed), errors.Is(err, previewservice.ErrRequestChanged):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "操作预览已失效，请重新预览"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取操作预览失败"))
	}
}
