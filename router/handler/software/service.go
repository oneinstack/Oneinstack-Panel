package software

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	softwareService "oneinstack/internal/services/software"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type componentServiceStatus struct {
	softwareService.ComponentServiceDefinition
	Installed        bool      `json:"installed"`
	RecordedVersion  string    `json:"recordedVersion,omitempty"`
	RuntimeVersion   string    `json:"runtimeVersion,omitempty"`
	State            string    `json:"state"`
	LoadState        string    `json:"loadState,omitempty"`
	ActiveState      string    `json:"activeState,omitempty"`
	SubState         string    `json:"subState,omitempty"`
	UnitFileState    string    `json:"unitFileState,omitempty"`
	CanReload        bool      `json:"canReload"`
	AvailableActions []string  `json:"availableActions"`
	PackageSource    string    `json:"packageSource,omitempty"`
	Busy             bool      `json:"busy"`
	ActiveTaskID     string    `json:"activeTaskId,omitempty"`
	ProbeErrorCode   string    `json:"probeErrorCode,omitempty"`
	ProbeError       string    `json:"probeError,omitempty"`
	CheckedAt        time.Time `json:"checkedAt"`
}

func ListComponentServices(c *gin.Context) {
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "数据库服务不可用"))
		return
	}
	statuses, err := componentServiceStatuses(c.Request.Context(), database)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取组件服务状态失败"))
		return
	}
	core.HandleSuccess(c, statuses)
}

func GetComponentService(c *gin.Context) {
	definition, err := softwareService.NormalizeServiceComponent(c.Param("component"))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "不支持的组件服务"))
		return
	}
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "数据库服务不可用"))
		return
	}
	statuses, err := componentServiceStatusesFor(
		c.Request.Context(),
		database,
		[]softwareService.ComponentServiceDefinition{definition},
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取组件服务状态失败"))
		return
	}
	for _, status := range statuses {
		if status.Component == definition.Component {
			core.HandleSuccess(c, status)
			return
		}
	}
	core.HandleError(c, core.NewError(core.ErrNotFound, "组件服务不存在"))
}

func RunComponentServiceAction(c *gin.Context) {
	definition, err := softwareService.NormalizeServiceComponent(c.Param("component"))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "不支持的组件服务"))
		return
	}
	var request input.SoftwareServiceAction
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	if !softwareService.IsServiceAction(request.Action) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "服务动作只能是 start、stop、restart 或 reload"))
		return
	}
	if request.Action == "reload" &&
		definition.Component != "nginx" && definition.Component != "php" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "该组件不支持安全重载，请使用重启"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	manager, err := getTaskManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "软件任务服务不可用"))
		return
	}
	task, err := manager.SubmitServiceAction(definition.Component, request.Action, userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建服务控制任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{
		"taskId":      task.ID,
		"operation":   task.Operation,
		"component":   task.Component,
		"softwareKey": task.SoftwareKey,
		"version":     task.RequestedVersion,
		"status":      task.Status,
		"progress":    task.Progress,
		"statusUrl":   "/v1/soft/tasks/" + task.ID,
		"streamUrl":   "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

func GetComponentServiceConfiguration(c *gin.Context) {
	definition, version, ok := installedConfigurationTarget(c)
	if !ok {
		return
	}
	configuration, err := softwareService.NewInstaller().InspectServiceConfiguration(
		c.Request.Context(),
		definition.Component,
		version,
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取组件配置失败"))
		return
	}
	core.HandleSuccess(c, configuration)
}

func PreviewComponentServiceConfiguration(c *gin.Context) {
	definition, version, ok := installedConfigurationTarget(c)
	if !ok {
		return
	}
	var request input.SoftwareServiceConfiguration
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	current, err := softwareService.NewInstaller().InspectServiceConfiguration(
		c.Request.Context(),
		definition.Component,
		version,
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取当前组件配置失败"))
		return
	}
	preview, err := softwareService.PreviewConfiguration(current, request.Revision, request.Values)
	if err != nil {
		message := "配置参数校验失败"
		if errors.Is(err, softwareService.ErrConfigurationConflict) {
			message = "配置已被其他操作修改，请刷新后重试"
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
		return
	}
	core.HandleSuccess(c, preview)
}

func ApplyComponentServiceConfiguration(c *gin.Context) {
	definition, version, ok := installedConfigurationTarget(c)
	if !ok {
		return
	}
	var request input.SoftwareServiceConfiguration
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	current, err := softwareService.NewInstaller().InspectServiceConfiguration(
		c.Request.Context(),
		definition.Component,
		version,
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取当前组件配置失败"))
		return
	}
	preview, err := softwareService.PreviewConfiguration(current, request.Revision, request.Values)
	if err != nil {
		message := "配置参数校验失败"
		if errors.Is(err, softwareService.ErrConfigurationConflict) {
			message = "配置已被其他操作修改，请刷新后重试"
		}
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
		return
	}
	if !preview.HasChanges {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "配置没有发生变化"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	manager, err := getTaskManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "软件任务服务不可用"))
		return
	}
	task, err := manager.SubmitConfiguration(
		definition.Component,
		preview.Revision,
		preview.Values,
		userID,
	)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建组件配置任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{
		"taskId":      task.ID,
		"operation":   task.Operation,
		"component":   task.Component,
		"softwareKey": task.SoftwareKey,
		"version":     task.RequestedVersion,
		"status":      task.Status,
		"progress":    task.Progress,
		"statusUrl":   "/v1/soft/tasks/" + task.ID,
		"streamUrl":   "/v1/soft/tasks/" + task.ID + "/events",
	}))
}

func installedConfigurationTarget(
	c *gin.Context,
) (softwareService.ComponentServiceDefinition, string, bool) {
	definition, err := softwareService.NormalizeServiceComponent(c.Param("component"))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "不支持的组件服务"))
		return softwareService.ComponentServiceDefinition{}, "", false
	}
	database := app.DB()
	if database == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "数据库服务不可用"))
		return softwareService.ComponentServiceDefinition{}, "", false
	}
	var installed models.Software
	if err := database.
		Where("`key` = ? AND installed = ?", definition.SoftwareKey, true).
		Order("install_time DESC").
		First(&installed).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.HandleError(c, core.NewError(core.ErrBadRequest, "组件尚未安装"))
		} else {
			core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取组件安装记录失败"))
		}
		return softwareService.ComponentServiceDefinition{}, "", false
	}
	version := strings.TrimSpace(installed.InstallVersion)
	if version == "" {
		version = strings.TrimSpace(installed.Version)
	}
	if version == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "组件安装版本缺失"))
		return softwareService.ComponentServiceDefinition{}, "", false
	}
	return definition, version, true
}

func componentServiceStatuses(
	ctx context.Context,
	database *gorm.DB,
) ([]componentServiceStatus, error) {
	return componentServiceStatusesFor(
		ctx,
		database,
		softwareService.SupportedComponentServices(),
	)
}

func componentServiceStatusesFor(
	ctx context.Context,
	database *gorm.DB,
	definitions []softwareService.ComponentServiceDefinition,
) ([]componentServiceStatus, error) {
	var installedRows []models.Software
	if err := database.
		Where("`key` IN ? AND installed = ?", []string{"webserver", "db", "php", "redis"}, true).
		Order("install_time DESC").
		Find(&installedRows).Error; err != nil {
		return nil, err
	}
	installedByKey := make(map[string]models.Software, len(installedRows))
	for _, row := range installedRows {
		if _, exists := installedByKey[row.Key]; !exists {
			installedByKey[row.Key] = row
		}
	}
	var activeTasks []models.SoftwareTask
	if err := database.
		Where("status IN ?", models.ActiveSoftwareTaskStatuses()).
		Order("created_at DESC").
		Find(&activeTasks).Error; err != nil {
		return nil, err
	}
	activeByComponent := make(map[string]models.SoftwareTask, len(activeTasks))
	for _, task := range activeTasks {
		if _, exists := activeByComponent[task.Component]; !exists {
			activeByComponent[task.Component] = task
		}
	}

	result := make([]componentServiceStatus, len(definitions))
	var probes sync.WaitGroup
	for index, definition := range definitions {
		now := time.Now().UTC()
		status := componentServiceStatus{
			ComponentServiceDefinition: definition,
			State:                      "not_installed",
			AvailableActions:           defaultServiceActions(definition.Component),
			CanReload:                  definition.Component == "nginx" || definition.Component == "php",
			CheckedAt:                  now,
		}
		if active, exists := activeByComponent[definition.Component]; exists {
			status.Busy = true
			status.ActiveTaskID = active.ID
		}
		installed, exists := installedByKey[definition.SoftwareKey]
		if !exists {
			result[index] = status
			continue
		}
		status.Installed = true
		status.RecordedVersion = strings.TrimSpace(installed.InstallVersion)
		if status.RecordedVersion == "" {
			status.RecordedVersion = strings.TrimSpace(installed.Version)
		}
		status.State = "unknown"
		probes.Add(1)
		go func(index int, definition softwareService.ComponentServiceDefinition, status componentServiceStatus) {
			defer probes.Done()
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			probe, probeErr := softwareService.NewInstaller().InspectService(
				probeCtx,
				definition.Component,
				status.RecordedVersion,
			)
			if probeErr != nil {
				status.ProbeErrorCode, status.ProbeError = softwareService.ClassifyServiceProbeError(probeErr)
				running, runningErr := softwareService.ComponentProcessRunning(probeCtx, definition.Component)
				if runningErr == nil {
					if running {
						status.State = "running"
					} else {
						status.State = "stopped"
					}
				}
				result[index] = status
				return
			}
			status.RuntimeVersion = probe.RuntimeVersion
			status.LoadState = probe.LoadState
			status.ActiveState = probe.ActiveState
			status.SubState = probe.SubState
			status.UnitFileState = probe.UnitFileState
			status.CanReload = probe.CanReload
			status.AvailableActions = probe.AvailableActions
			status.PackageSource = probe.PackageSource
			status.State = serviceState(probe.ActiveState)
			result[index] = status
		}(index, definition, status)
	}
	probes.Wait()
	return result, nil
}

func defaultServiceActions(component string) []string {
	result := []string{"start", "stop", "restart"}
	if component == "nginx" || component == "php" {
		result = append(result, "reload")
	}
	return result
}

func serviceState(activeState string) string {
	switch activeState {
	case "active":
		return "running"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	case "activating", "deactivating", "reloading":
		return "transitioning"
	default:
		return "unknown"
	}
}
