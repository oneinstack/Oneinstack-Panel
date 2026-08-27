package container

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/core"
	"oneinstack/internal/i18n"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	auditservice "oneinstack/internal/services/audit"
	containerService "oneinstack/internal/services/container"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

var service = containerService.New()
var createTaskManager = containerService.NewCreateTaskManager(service)

func StartCreateTaskManager() error {
	return createTaskManager.Start()
}

func Runtime(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	status := service.Runtime(ctx)
	status.Message = i18n.LocalizeStatusText(c.GetString("locale"), status.Message, !status.Available)
	core.HandleSuccess(c, status)
}

func RuntimeAction(c *gin.Context) {
	var request input.ContainerRuntimeActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.RuntimeAction(ctx, request.Action, request.Confirm); err != nil {
		recordAction(c, "container.runtime."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.runtime."+strings.ToLower(request.Action), http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"action": request.Action})
}

func ListContainers(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := service.ListContainers(ctx)
	if err != nil {
		operationError(c, err)
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if search != "" && !strings.Contains(strings.ToLower(stringValue(item, "Names")+" "+stringValue(item, "Image")), search) {
			continue
		}
		if status != "" && status != "all" && !strings.Contains(strings.ToLower(stringValue(item, "Status")), status) {
			continue
		}
		filtered = append(filtered, item)
	}
	core.HandleSuccess(c, gin.H{"items": filtered, "total": len(filtered)})
}
func GetContainer(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.InspectContainer(ctx, c.Param("id"))
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}
func ContainerStats(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.Stats(ctx, c.Param("id"))
	if err != nil {
		containerStatsError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}
func ListImages(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := service.ListImages(ctx)
	if err != nil {
		operationError(c, err)
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if search != "" && !strings.Contains(strings.ToLower(stringValue(item, "ID")+" "+stringValue(item, "Repository")+":"+stringValue(item, "Tag")), search) {
			continue
		}
		item["used"] = imageUsed(item["Containers"])
		filtered = append(filtered, item)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	core.HandleSuccess(c, gin.H{"items": filtered[start:end], "total": len(filtered), "page": page, "pageSize": pageSize})
}
func ListNetworks(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := service.ListNetworks(ctx)
	if err != nil {
		operationError(c, err)
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if search != "" && !strings.Contains(strings.ToLower(stringValue(item, "Name")+" "+stringValue(item, "Driver")+" "+stringValue(item, "Subnet")), search) {
			continue
		}
		filtered = append(filtered, item)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	core.HandleSuccess(c, gin.H{"items": filtered[start:end], "total": len(filtered), "page": page, "pageSize": pageSize})
}
func ListVolumes(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := service.ListVolumes(ctx)
	if err != nil {
		operationError(c, err)
		return
	}
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if search != "" && !strings.Contains(strings.ToLower(stringValue(item, "Name")+" "+stringValue(item, "Driver")+" "+stringValue(item, "Mountpoint")), search) {
			continue
		}
		filtered = append(filtered, item)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 10
	}
	start := (page - 1) * pageSize
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + pageSize
	if end > len(filtered) {
		end = len(filtered)
	}
	core.HandleSuccess(c, gin.H{"items": filtered[start:end], "total": len(filtered), "page": page, "pageSize": pageSize})
}
func ListCompose(c *gin.Context) { list(c, service.ListComposeProjects) }

func InspectImage(c *gin.Context)   { inspectResource(c, service.InspectImage) }
func InspectNetwork(c *gin.Context) { inspectResource(c, service.InspectNetwork) }
func InspectVolume(c *gin.Context)  { inspectResource(c, service.InspectVolume) }

func inspectResource(c *gin.Context, loader func(context.Context, string) (map[string]any, error)) {
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := loader(ctx, c.Param("id"))
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func Cleanup(c *gin.Context) {
	var request input.ContainerCleanupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.CleanupContainers(ctx, request.Confirm)
	if err != nil {
		recordAction(c, "container.dangerous.cleanup", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.dangerous.cleanup", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"action": "container.dangerous.cleanup", "output": result})
}

func PruneImages(c *gin.Context)   { prune(c, "image", service.PruneImages) }
func PruneNetworks(c *gin.Context) { prune(c, "network", service.PruneNetworks) }
func PruneVolumes(c *gin.Context)  { prune(c, "volume", service.PruneVolumes) }

func prune(c *gin.Context, kind string, action func(context.Context, bool) (string, error)) {
	var request input.ContainerCleanupRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := action(ctx, request.Confirm)
	if err != nil {
		recordAction(c, "container."+kind+".cleanup", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container."+kind+".cleanup", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"action": "container." + kind + ".cleanup", "output": result})
}

func CreateNetwork(c *gin.Context) {
	var request input.ContainerNetworkRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	err := service.CreateNetwork(ctx, containerService.NetworkCreateRequest{
		Name: request.Name, Driver: request.Driver, IPv4: request.IPv4,
		IPv4Subnet: request.IPv4Subnet, IPv4Gateway: request.IPv4Gateway, IPv4IPRange: request.IPv4IPRange, IPv4AuxAddresses: request.IPv4AuxAddresses,
		IPv6: request.IPv6, IPv6Subnet: request.IPv6Subnet, IPv6Gateway: request.IPv6Gateway, IPv6IPRange: request.IPv6IPRange, IPv6AuxAddresses: request.IPv6AuxAddresses,
		Options: request.Options, Labels: request.Labels, OptionsText: request.OptionsText, LabelsText: request.LabelsText,
	})
	if err != nil {
		recordAction(c, "container.network.create", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.network.create", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"name": request.Name, "action": "container.network.create"})
}
func DeleteNetwork(c *gin.Context) {
	confirm := strings.EqualFold(c.Query("confirm"), "true")
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.DeleteNetwork(ctx, c.Param("id"), confirm); err != nil {
		operationError(c, err)
		return
	}
	recordAction(c, "container.network.delete", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "action": "container.network.delete"})
}

func BatchDeleteNetwork(c *gin.Context) {
	var request input.ContainerBatchDeleteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	items := make([]gin.H, 0, len(request.IDs))
	for _, id := range request.IDs {
		item := gin.H{"id": id}
		if err := service.DeleteNetwork(ctx, id, request.Confirm); err != nil {
			errorCode, status := core.ErrInternalError, http.StatusInternalServerError
			if errors.Is(err, containerService.ErrProtectedNetwork) {
				errorCode, status = core.ErrResourceStateInvalid, http.StatusConflict
			}
			item["success"], item["error"], item["errorCode"] = false, containerBatchError(c, err), errorCode
			recordAction(c, "container.network.delete", status, err)
		} else {
			item["success"] = true
			recordAction(c, "container.network.delete", http.StatusOK, nil)
		}
		items = append(items, item)
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}
func CreateVolume(c *gin.Context) { resourceAction(c, "volume", service.CreateVolume) }
func DeleteVolume(c *gin.Context) {
	confirm := strings.EqualFold(c.Query("confirm"), "true")
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.DeleteVolume(ctx, c.Param("id"), confirm); err != nil {
		operationError(c, err)
		return
	}
	recordAction(c, "container.volume.delete", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "action": "container.volume.delete"})
}

func BatchDeleteVolume(c *gin.Context) {
	var request input.ContainerBatchDeleteRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	items := make([]gin.H, 0, len(request.IDs))
	for _, id := range request.IDs {
		item := gin.H{"id": id}
		if err := service.DeleteVolume(ctx, id, request.Confirm); err != nil {
			item["success"], item["error"], item["errorCode"] = false, containerBatchError(c, err), core.ErrInternalError
			recordAction(c, "container.volume.delete", http.StatusInternalServerError, err)
		} else {
			item["success"] = true
			recordAction(c, "container.volume.delete", http.StatusOK, nil)
		}
		items = append(items, item)
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}

func CreateContainer(c *gin.Context) {
	var request input.ContainerCreateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	createRequest := containerService.ContainerCreateRequest{
		Ports:  make([]containerService.PortMapping, 0, len(request.Ports)),
		Mounts: make([]containerService.Mount, 0, len(request.Mounts)),
		Name:   request.Name, Image: request.Image, Networks: request.Networks, IPv4: request.IPv4, IPv6: request.IPv6,
		Command: request.Command, Entrypoint: request.Entrypoint, AutoRemove: request.AutoRemove, Privileged: request.Privileged,
		TTY: request.TTY, OpenStdin: request.OpenStdin, Restart: request.Restart, CPUWeight: request.CPUWeight, CPULimit: request.CPULimit,
		MemoryLimitMB: request.MemoryLimitMB, Labels: request.Labels, Environment: request.Environment,
	}
	for _, port := range request.Ports {
		createRequest.Ports = append(createRequest.Ports, containerService.PortMapping{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol})
	}
	for _, mount := range request.Mounts {
		mountType := strings.TrimSpace(mount.Type)
		if mountType == "" {
			mountType = strings.TrimSpace(mount.Mode)
		}
		createRequest.Mounts = append(createRequest.Mounts, containerService.Mount{Type: mountType, Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{Operation: models.ContainerTaskOperationCreate, Create: &createRequest, Image: request.Image}, userID)
	if err != nil {
		recordAction(c, "container.create", http.StatusBadRequest, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.create", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func GetContainerCreateTask(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	task, err := createTaskManager.Get(c.Param("taskId"), userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "容器创建任务不存在"))
		return
	}
	core.HandleSuccess(c, task)
}

func containerTaskResponse(task *models.ContainerTask) gin.H {
	return gin.H{"taskId": task.ID, "operation": task.Operation, "status": task.Status, "progress": task.Progress,
		"statusUrl": "/v1/containers/tasks/" + task.ID, "streamUrl": "/v1/containers/tasks/" + task.ID + "/events"}
}

func ListContainerTasks(c *gin.Context) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	access, _ := middleware.UserAccess(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	result, err := createTaskManager.List(containerService.TaskListOptions{RequestedBy: userID, IncludeAll: canReadAllContainerTasks(access), ActiveOnly: strings.EqualFold(c.Query("active"), "true"), Operation: c.Query("operation"), Status: c.Query("status"), Page: page, PageSize: pageSize})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取容器任务失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetContainerTask(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	task, err := createTaskManager.Get(c.Param("id"), userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "容器任务不存在"))
		return
	}
	core.HandleSuccess(c, task)
}

func StreamContainerTaskEvents(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	task, err := createTaskManager.Get(c.Param("id"), userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "容器任务不存在"))
		return
	}
	after, _ := strconv.ParseInt(c.DefaultQuery("after", "0"), 10, 64)
	if header := c.GetHeader("Last-Event-ID"); header != "" {
		if value, parseErr := strconv.ParseInt(header, 10, 64); parseErr == nil && value > after {
			after = value
		}
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.Writer.Flush()
	notifications, unsubscribe := createTaskManager.Subscribe(task.ID)
	defer unsubscribe()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		events, eventErr := createTaskManager.EventsAfter(task.ID, after, 200)
		if eventErr != nil {
			return
		}
		for i := range events {
			data, marshalErr := json.Marshal(containerTaskEventResponse(c.GetString("locale"), &events[i]))
			if marshalErr != nil {
				continue
			}
			_, writeErr := fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", events[i].Seq, events[i].Type, data)
			if writeErr != nil {
				return
			}
			after = events[i].Seq
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}
		current, getErr := createTaskManager.Get(task.ID, userID, canReadAllContainerTasks(access))
		if getErr != nil {
			return
		}
		if models.IsContainerTaskTerminal(current.Status) && after >= current.EventSeq {
			return
		}
		select {
		case <-c.Request.Context().Done():
			return
		case <-notifications:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func containerTaskEventResponse(locale string, event *models.ContainerTaskEvent) gin.H {
	failed := strings.EqualFold(event.Level, "error") || strings.EqualFold(event.Status, "failed")
	result := gin.H{"seq": event.Seq, "type": event.Type, "level": event.Level, "status": event.Status, "phase": event.Phase, "progress": event.Progress, "message": i18n.LocalizeStatusText(locale, event.Message, failed), "log": event.Log, "code": event.Code, "createdAt": event.CreatedAt}
	if event.PhaseProgress != nil {
		result["phaseProgress"] = *event.PhaseProgress
	}
	if strings.TrimSpace(event.DetailsJSON) != "" {
		var details any
		if json.Unmarshal([]byte(event.DetailsJSON), &details) == nil {
			result["details"] = details
		}
	}
	return result
}

func GetContainerTaskLog(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	cursor, _ := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	limit, _ := strconv.ParseInt(c.DefaultQuery("limit", "65536"), 10, 64)
	chunk, err := createTaskManager.ReadLog(c.Param("id"), cursor, limit, userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "读取容器任务日志失败"))
		return
	}
	core.HandleSuccess(c, chunk)
}

func DownloadContainerTaskLog(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	file, info, err := createTaskManager.OpenLog(c.Param("id"), userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "容器任务日志不存在"))
		return
	}
	defer file.Close()
	name := "oneinstack-container-task-" + c.Param("id") + ".log"
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Header("Content-Type", "text/plain; charset=utf-8")
	http.ServeContent(c.Writer, c.Request, name, info.ModTime(), file)
}

func CancelContainerTask(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	if access == nil || (!access.HasPermission(accessservice.PermissionContainerWrite) && !access.HasPermission(accessservice.PermissionContainerImageWrite)) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权取消该容器任务"))
		return
	}
	task, err := createTaskManager.Cancel(c.Param("id"), userID, canReadAllContainerTasks(access))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "取消容器任务失败"))
		return
	}
	recordAction(c, "container.task.cancel", http.StatusOK, nil)
	core.HandleSuccess(c, task)
}

func canReadAllContainerTasks(access *accessservice.UserAccess) bool {
	return access != nil && (access.IsSuperAdmin || access.HasPermission(accessservice.PermissionTaskReadAll))
}

func BatchAction(c *gin.Context) {
	var request input.ContainerBatchActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	items := make([]gin.H, 0, len(request.IDs))
	for _, id := range request.IDs {
		item := gin.H{"id": id, "action": request.Action}
		if err := service.ContainerAction(ctx, id, request.Action, request.Force, request.Confirm); err != nil {
			item["success"] = false
			item["error"] = containerBatchError(c, err)
			item["errorCode"] = core.ErrInternalError
			recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
		} else {
			item["success"] = true
			if state, failed, err := observeContainerActionState(ctx, c, id, request.Action); err != nil {
				item["success"] = false
				item["error"] = containerBatchError(c, err)
				item["errorCode"] = core.ErrInternalError
				recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
			} else if state != nil {
				for key, value := range state {
					item[key] = value
				}
				if failed {
					item["success"] = false
					item["error"] = state["stateMessage"]
					item["errorCode"] = core.ErrOperationFailed
					recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusConflict, errors.New(state["stateMessage"].(string)))
				}
			}
			if item["success"] == true {
				recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusOK, nil)
			}
		}
		items = append(items, item)
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}

func containerBatchError(c *gin.Context, err error) string {
	if errors.Is(err, containerService.ErrProtectedNetwork) {
		response := core.ErrorResponseForLocale(protectedNetworkError(), c.GetString("locale"))
		return response.Message
	}
	response := core.ErrorResponseForLocale(
		core.WrapError(err, core.ErrInternalError, containerOperationMessage(c)),
		c.GetString("locale"),
	)
	return response.Message
}

func protectedNetworkError() *core.AppError {
	return core.NewErrorWithDetail(
		core.ErrResourceStateInvalid,
		"Docker内置网络不可删除",
		"bridge、host 和 none 是 Docker 创建的内置网络，不能删除。请删除自定义网络，或使用“清理无用网络”清理未使用的自定义网络。",
	)
}

func Action(c *gin.Context) {
	var request input.ContainerActionRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.ContainerAction(ctx, c.Param("id"), request.Action, request.Force, request.Confirm); err != nil {
		recordAction(c, "container."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	state, failed, err := observeContainerActionState(ctx, c, c.Param("id"), request.Action)
	if err != nil {
		recordAction(c, "container."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	if failed {
		recordAction(c, "container."+strings.ToLower(request.Action), http.StatusConflict, errors.New(state["stateMessage"].(string)))
	} else {
		recordAction(c, "container."+strings.ToLower(request.Action), http.StatusOK, nil)
	}
	result := gin.H{"id": c.Param("id"), "action": request.Action}
	for key, value := range state {
		result[key] = value
	}
	core.HandleSuccess(c, result)
}

func observeContainerActionState(ctx context.Context, c *gin.Context, id, action string) (gin.H, bool, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	if action != "start" && action != "restart" {
		return nil, false, nil
	}
	observed, err := service.ObserveContainerAction(ctx, id)
	if err != nil {
		return nil, false, err
	}
	failed := !observed.Running && (observed.Status == "exited" || observed.Status == "dead" || observed.Status == "created")
	result := gin.H{
		"status":        observed.Status,
		"running":       observed.Running,
		"paused":        observed.Paused,
		"exitCode":      observed.ExitCode,
		"stateObserved": true,
		"stateFailed":   failed,
	}
	if failed {
		result["stateCode"] = "CONTAINER_STARTUP_EXITED"
		result["stateMessage"] = containerStartupFailureMessage(c)
	}
	return result, failed, nil
}

func containerStartupFailureMessage(c *gin.Context) string {
	if i18n.Canonical(c.GetString("locale")) == i18n.LocaleEnUS {
		return "The container exited after startup; check the container logs and startup configuration."
	}
	return "容器启动后进程已退出，请检查容器日志和启动配置。"
}

func Logs(c *gin.Context) {
	options, follow, err := containerLogOptions(c)
	if err != nil {
		containerLogBadRequest(c, err)
		return
	}
	if follow {
		followContainerLogs(c, options)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.Logs(ctx, c.Param("id"), options)
	if err != nil {
		recordAction(c, "container.logs.read", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "logs": result})
}

func DownloadLogs(c *gin.Context) {
	options, follow, err := containerLogOptions(c)
	if err != nil {
		containerLogBadRequest(c, err)
		return
	}
	if follow {
		containerLogBadRequest(c, errors.New("日志下载不支持 follow=true"))
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.Logs(ctx, c.Param("id"), options)
	if err != nil {
		recordAction(c, "container.logs.download", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	name := fmt.Sprintf("oneinstack-container-%s-%s.log", safeDownloadName(c.Param("id")), time.Now().UTC().Format("20060102-150405"))
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(result))
}

func containerLogOptions(c *gin.Context) (containerService.LogOptions, bool, error) {
	tail, err := strconv.Atoi(c.DefaultQuery("tail", "500"))
	if err != nil {
		return containerService.LogOptions{}, false, errors.New("tail 必须是 1 到 10000 的整数")
	}
	timestamps, err := containerLogBool(c.Query("timestamps"), true, "timestamps")
	if err != nil {
		return containerService.LogOptions{}, false, err
	}
	follow, err := containerLogBool(c.Query("follow"), false, "follow")
	if err != nil {
		return containerService.LogOptions{}, false, err
	}
	options := containerService.LogOptions{
		Tail:       tail,
		Since:      strings.TrimSpace(c.Query("since")),
		Until:      strings.TrimSpace(c.Query("until")),
		Timestamps: timestamps,
	}
	if err := containerService.ValidateLogOptions(options); err != nil {
		return containerService.LogOptions{}, false, err
	}
	return options, follow, nil
}

func containerLogBool(value string, defaultValue bool, field string) (bool, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultValue, nil
	}
	if value == "true" {
		return true, nil
	}
	if value == "false" {
		return false, nil
	}
	return false, fmt.Errorf("%s 必须是 true 或 false", field)
}

func containerLogBadRequest(c *gin.Context, err error) {
	detail := strings.TrimSpace(strings.TrimPrefix(err.Error(), containerService.ErrInvalidLogOptions.Error()+":"))
	core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "容器日志查询参数格式不正确", detail))
}

func followContainerLogs(c *gin.Context, options containerService.LogOptions) {
	containerID := c.Param("id")
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.Writer.Flush()

	stream := &containerLogSSEWriter{writer: c.Writer}
	result := make(chan error, 1)
	go func() {
		result <- service.FollowLogs(c.Request.Context(), containerID, options, stream)
	}()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case err := <-result:
			_ = stream.FlushPending()
			if err != nil && !errors.Is(err, context.Canceled) {
				recordAction(c, "container.logs.follow", http.StatusInternalServerError, err)
				_ = stream.Event("error", gin.H{"message": i18n.LocalizeBusinessText(c.GetString("locale"), "容器日志追踪已中断，请检查容器状态和 Docker 运行时")})
				return
			}
			_ = stream.Event("end", gin.H{"message": i18n.LocalizeBusinessText(c.GetString("locale"), "容器日志追踪已结束")})
			return
		case <-heartbeat.C:
			if err := stream.Heartbeat(); err != nil {
				return
			}
		}
	}
}

type containerLogSSEWriter struct {
	mu     sync.Mutex
	writer gin.ResponseWriter
	buffer []byte
}

func (writer *containerLogSSEWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	written := len(data)
	writer.buffer = append(writer.buffer, data...)
	for {
		newline := bytes.IndexByte(writer.buffer, '\n')
		if newline >= 0 {
			line := strings.TrimSuffix(string(writer.buffer[:newline]), "\r")
			writer.buffer = writer.buffer[newline+1:]
			if err := writer.eventLocked("log", gin.H{"line": line}); err != nil {
				return 0, err
			}
			continue
		}
		if len(writer.buffer) > 64*1024 {
			line := string(writer.buffer[:64*1024])
			writer.buffer = writer.buffer[64*1024:]
			if err := writer.eventLocked("log", gin.H{"line": line, "partial": true}); err != nil {
				return 0, err
			}
			continue
		}
		return written, nil
	}
}

func (writer *containerLogSSEWriter) FlushPending() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if len(writer.buffer) == 0 {
		return nil
	}
	line := string(writer.buffer)
	writer.buffer = nil
	return writer.eventLocked("log", gin.H{"line": line})
}

func (writer *containerLogSSEWriter) Event(event string, payload any) error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.eventLocked(event, payload)
}

func (writer *containerLogSSEWriter) Heartbeat() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if _, err := fmt.Fprint(writer.writer, ": heartbeat\n\n"); err != nil {
		return err
	}
	writer.writer.Flush()
	return nil
}

func (writer *containerLogSSEWriter) eventLocked(event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer.writer, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}
	writer.writer.Flush()
	return nil
}

func safeDownloadName(value string) string {
	return strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '.' || char == '-' || char == '_' {
			return char
		}
		return '_'
	}, value)
}

func PullImage(c *gin.Context) {
	var request input.ContainerImagePullRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	reference, err := service.RegistryImageReference(request.RegistryID, request.ImageName, request.Reference)
	if err != nil {
		badRequest(c, err)
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{Operation: models.ContainerTaskOperationPull, Image: reference}, userID)
	if err != nil {
		recordAction(c, "container.image.pull", http.StatusBadRequest, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.pull", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func ImportImage(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		badRequest(c, errors.New("请上传镜像文件，字段名为 file"))
		return
	}
	if file.Size <= 0 || file.Size > 4<<30 {
		badRequest(c, errors.New("镜像文件大小必须在0至4GiB之间"))
		return
	}
	temporary, err := os.CreateTemp("", "oneinstack-docker-image-*.tar")
	if err != nil {
		operationError(c, err)
		return
	}
	path := temporary.Name()
	if err := temporary.Close(); err != nil {
		os.Remove(path)
		operationError(c, err)
		return
	}
	defer os.Remove(path)
	if err := c.SaveUploadedFile(file, path); err != nil {
		operationError(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.LoadImage(ctx, path); err != nil {
		recordAction(c, "container.image.import", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.import", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{"action": "container.image.import"}))
}

func TagImage(c *gin.Context) {
	var request input.ContainerImageTagRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.TagImage(ctx, c.Param("id"), request.Reference, request.RemoveOther, request.Confirm); err != nil {
		recordAction(c, "container.image.tag", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.tag", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "reference": request.Reference})
}

func PushImage(c *gin.Context) {
	var request input.ContainerImagePushRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	reference, err := service.RegistryImageReference(request.RegistryID, request.ImageName, request.Reference)
	if err != nil {
		badRequest(c, err)
		return
	}
	if err := service.PushImage(ctx, reference); err != nil {
		recordAction(c, "container.image.push", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.push", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{"action": "container.image.push", "reference": reference}))
}

func BuildImage(c *gin.Context) {
	var request input.ContainerImageBuildRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Submit(containerService.TaskRequest{Operation: models.ContainerTaskOperationBuild, Image: request.Name, Build: &containerService.BuildTaskRequest{Name: request.Name, Dockerfile: request.Dockerfile, ContextPath: request.ContextPath, DockerfilePath: request.DockerfilePath, Labels: request.Labels, LabelsText: request.LabelsText}}, userID)
	if err != nil {
		recordAction(c, "container.image.build", http.StatusBadRequest, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.build", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, containerTaskResponse(task)))
}

func PruneBuildCache(c *gin.Context) { prune(c, "image.build-cache", service.PruneBuildCache) }

func ExportImage(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	data, err := service.ExportImage(ctx, c.Param("id"))
	if err != nil {
		operationError(c, err)
		return
	}
	c.Header("Content-Type", "application/x-tar")
	c.Header("Content-Disposition", `attachment; filename="image.tar"`)
	c.Data(http.StatusOK, "application/x-tar", data)
}

func DeleteImage(c *gin.Context) {
	confirm := strings.EqualFold(c.Query("confirm"), "true")
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.DeleteImage(ctx, c.Param("id"), confirm); err != nil {
		recordAction(c, "container.image.delete", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.delete", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "action": "container.image.delete"})
}

func Config(c *gin.Context) {
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.Config(ctx)
	if err != nil {
		recordAction(c, "container.config.read", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func SaveConfig(c *gin.Context) {
	var request input.ContainerConfigRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	var result map[string]any
	var err error
	if strings.TrimSpace(request.Raw) != "" {
		result, err = service.SaveConfig(ctx, request.Raw)
	} else {
		result, err = service.SaveBasicConfig(ctx, request.Basic)
	}
	if err != nil {
		recordAction(c, "container.config.change", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.config.change", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func Registries(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	ctx, cancel := requestContext(c)
	defer cancel()
	items, total, err := service.ListRegistries(ctx, c.Query("search"), page, pageSize)
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{
		"items":        items,
		"total":        total,
		"page":         page,
		"pageSize":     pageSize,
		"capabilities": service.Capabilities(ctx),
	})
}

func CreateRegistry(c *gin.Context) {
	var request input.ContainerRegistryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.CreateRegistry(ctx, containerService.RegistryInput{
		Name: request.Name, Address: request.Address, Protocol: request.Protocol,
		AuthEnabled: request.AuthEnabled, Username: request.Username, Password: request.Password,
	})
	if err != nil {
		recordAction(c, "container.registry.create", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.registry.create", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func UpdateRegistry(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		badRequest(c, errors.New("仓库 ID 无效"))
		return
	}
	var request input.ContainerRegistryRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.UpdateRegistry(ctx, uint(id), containerService.RegistryInput{
		Name: request.Name, Address: request.Address, Protocol: request.Protocol,
		AuthEnabled: request.AuthEnabled, Username: request.Username, Password: request.Password,
	})
	if err != nil {
		recordAction(c, "container.registry.update", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.registry.update", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func DeleteRegistry(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		badRequest(c, errors.New("仓库 ID 无效"))
		return
	}
	confirm := strings.EqualFold(c.Query("confirm"), "true")
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.DeleteRegistry(ctx, uint(id), confirm); err != nil {
		recordAction(c, "container.registry.delete", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.registry.delete", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": id, "action": "container.registry.delete"})
}

func TestRegistry(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		badRequest(c, errors.New("仓库 ID 无效"))
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.TestRegistry(ctx, uint(id))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, containerService.ErrRegistryProbeFailed) {
			status = http.StatusBadGateway
		}
		recordAction(c, "container.registry.test", status, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.registry.test", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func Templates(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "10"))
	ctx, cancel := requestContext(c)
	defer cancel()
	items, total, err := service.ListTemplates(ctx, c.Query("search"), page, pageSize)
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": total, "page": page, "pageSize": pageSize})
}

func GetTemplate(c *gin.Context) {
	id, err := parseContainerID(c.Param("id"), "模板")
	if err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.GetTemplate(ctx, id)
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func CreateTemplate(c *gin.Context) {
	var request input.ContainerComposeTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.CreateTemplate(ctx, request.Name, request.Description, request.Content)
	if err != nil {
		recordAction(c, "container.compose.template.create", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.compose.template.create", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func UpdateTemplate(c *gin.Context) {
	id, err := parseContainerID(c.Param("id"), "模板")
	if err != nil {
		badRequest(c, err)
		return
	}
	var request input.ContainerComposeTemplateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.UpdateTemplate(ctx, id, request.Name, request.Description, request.Content)
	if err != nil {
		recordAction(c, "container.compose.template.update", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.compose.template.update", http.StatusOK, nil)
	core.HandleSuccess(c, result)
}

func DeleteTemplate(c *gin.Context) {
	id, err := parseContainerID(c.Param("id"), "模板")
	if err != nil {
		badRequest(c, err)
		return
	}
	confirm := strings.EqualFold(c.Query("confirm"), "true")
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.DeleteTemplate(ctx, id, confirm); err != nil {
		recordAction(c, "container.compose.template.delete", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.compose.template.delete", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": id, "action": "container.compose.template.delete"})
}

func parseContainerID(value, label string) (uint, error) {
	id, err := strconv.ParseUint(value, 10, 32)
	if err != nil || id == 0 {
		return 0, errors.New(label + " ID 无效")
	}
	return uint(id), nil
}

func list(c *gin.Context, loader func(context.Context) ([]map[string]any, error)) {
	ctx, cancel := requestContext(c)
	defer cancel()
	items, err := loader(ctx)
	if err != nil {
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
}

func stringValue(item map[string]any, key string) string {
	value, _ := item[key].(string)
	return value
}

func imageUsed(value any) bool {
	switch containers := value.(type) {
	case float64:
		return containers > 0
	case int:
		return containers > 0
	case int64:
		return containers > 0
	case string:
		count, err := strconv.Atoi(strings.TrimSpace(containers))
		return err == nil && count > 0
	default:
		return false
	}
}

func resourceAction(c *gin.Context, kind string, action func(context.Context, containerService.ResourceRequest) error) {
	var request input.ContainerResourceRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	err := action(ctx, containerService.ResourceRequest{
		Name: request.Name, Driver: request.Driver, Options: request.Options, Labels: request.Labels,
		OptionsText: request.OptionsText, LabelsText: request.LabelsText, NFS: request.NFS,
	})
	if err != nil {
		operationError(c, err)
		return
	}
	recordAction(c, "container."+kind+".create", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"name": request.Name, "action": "container." + kind + ".create"})
}

func requestContext(c *gin.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(c.Request.Context(), 65*time.Second)
}

func badRequest(c *gin.Context, err error) {
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "容器资源参数格式不正确"))
}

func operationError(c *gin.Context, err error) {
	if errors.Is(err, containerService.ErrProtectedNetwork) {
		core.HandleError(c, protectedNetworkError())
		return
	}
	if errors.Is(err, containerService.ErrContainerInspectUnavailable) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"容器详情正在刷新，请稍后重试",
			"Docker 在容器重启期间未返回完整详情；请保留当前详情并稍后重新加载。",
		))
		return
	}
	if errors.Is(err, containerService.ErrInvalidLogOptions) {
		containerLogBadRequest(c, err)
		return
	}
	if errors.Is(err, containerService.ErrInvalidRegistryInput) {
		detail := strings.TrimSpace(strings.TrimPrefix(err.Error(), containerService.ErrInvalidRegistryInput.Error()+": "))
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInvalidParameter,
			"容器镜像仓库参数无效",
			detail,
		))
		return
	}
	if errors.Is(err, containerService.ErrRegistryProbeFailed) {
		core.HandleErrorWithStatus(c, http.StatusBadGateway, registryProbeError(err))
		return
	}
	if errors.Is(err, containerService.ErrInvalidContainerConfig) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrBadRequest,
			"容器配置无效",
			strings.TrimPrefix(err.Error(), containerService.ErrInvalidContainerConfig.Error()+": "),
		))
		return
	}
	if errors.Is(err, containerService.ErrRuntimeUnavailable) {
		detail := strings.TrimSpace(strings.TrimPrefix(err.Error(), containerService.ErrRuntimeUnavailable.Error()+": "))
		if strings.Contains(detail, "executable file not found in PATH") {
			detail = "未找到 Docker 可执行文件（docker），请安装 Docker CLI/Engine，并确认 docker 已加入面板进程的 PATH。"
		} else if detail != "" {
			detail = "Docker 客户端已安装，但无法连接 Docker 守护进程；请确认 Docker 服务已启动，并检查当前面板运行用户是否有访问 Docker socket 的权限。"
		} else {
			detail = "无法确认 Docker 运行时状态；请检查 Docker 是否安装、服务是否启动，以及面板进程的 PATH 和 Docker socket 权限。"
		}
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrContainerRuntimeUnavailable,
			"Docker运行时不可用，请先安装并启动 Docker 后重试",
			detail,
		))
		return
	}
	if errors.Is(err, containerService.ErrImagePullFailed) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInternalError,
			"Docker镜像拉取失败",
			err.Error(),
		))
		return
	}
	if errors.Is(err, containerService.ErrDockerCommandTimeout) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInternalError,
			"Docker操作超时",
			err.Error(),
		))
		return
	}
	core.HandleError(c, core.WrapError(err, core.ErrInternalError, containerOperationMessage(c)))
}

func containerStatsError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, containerService.ErrContainerStatsInvalidReference):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrInvalidParameter,
			"容器标识无效",
			"容器标识不能为空、不能包含换行符，且不能以短横线开头。",
		))
	case errors.Is(err, containerService.ErrContainerStatsTimeout):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrTaskTimeout,
			"Docker stats 读取超时",
			"Docker stats 未在限定时间内返回容器实时指标，请检查 Docker daemon 状态、容器运行状态和面板请求超时设置后重试。",
		))
	case errors.Is(err, containerService.ErrContainerStatsPermissionDenied):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrPermissionUnavailable,
			"Docker stats 权限不足",
			"面板进程无权访问 Docker daemon 或 Docker socket，请检查面板运行用户、Docker socket 所属用户组和访问权限。",
		))
	case errors.Is(err, containerService.ErrContainerStatsNotFound):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrNotFound,
			"目标容器不存在或已被删除",
			"Docker 未找到该容器，请刷新容器列表后重新打开详情。",
		))
	case errors.Is(err, containerService.ErrContainerStatsEmpty):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrResourceStateInvalid,
			"容器实时指标正在刷新，请稍后重试",
			"Docker 未返回完整容器实时指标，容器可能正在重启或 Docker daemon 尚未完成统计采样。",
		))
	case errors.Is(err, containerService.ErrRuntimeUnavailable):
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrContainerRuntimeUnavailable,
			"Docker 运行时不可用，无法读取实时指标",
			"Docker daemon 当前不可用，请确认 Docker 服务已启动，并检查面板运行用户是否有访问 Docker socket 的权限。",
		))
	default:
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrServiceUnavailable,
			"Docker stats 读取失败",
			"Docker 未能返回容器实时指标，请检查 Docker daemon、容器状态和面板运行用户权限后重试。",
		))
	}
}

func registryProbeError(err error) *core.AppError {
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(lower, "no such host"), strings.Contains(lower, "server misbehaving"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库域名解析失败", "无法解析仓库域名，请检查仓库地址以及面板服务器的 DNS 配置。")
	case strings.Contains(lower, "connection refused"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库拒绝连接", "目标地址可访问，但仓库服务未接受连接；请检查服务、端口和防火墙配置。")
	case strings.Contains(lower, "network is unreachable"), strings.Contains(lower, "no route to host"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库网络不可达", "面板服务器当前没有到仓库的可用网络路由，请检查网络、路由和防火墙配置。")
	case strings.Contains(lower, "timeout"), strings.Contains(lower, "timed out"), strings.Contains(lower, "deadline exceeded"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库连接测试超时", "仓库未在限定时间内响应，请检查网络连通性和仓库服务状态。")
	case strings.Contains(lower, "x509"), strings.Contains(lower, "certificate"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库 TLS 证书校验失败", "请检查仓库证书的有效期、域名匹配和证书信任链。")
	case strings.Contains(lower, "http 401"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库身份认证失败", "仓库要求有效身份凭据，请检查是否启用认证以及用户名和密码是否正确。")
	case strings.Contains(lower, "http 403"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库拒绝当前访问", "仓库已收到请求但拒绝访问，请检查账号权限和仓库访问策略。")
	case strings.Contains(lower, "仓库返回 http"):
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库接口响应异常", "目标地址未返回可接受的 Registry V2 响应，请确认地址和仓库服务配置。")
	default:
		return core.NewErrorWithDetail(core.ErrServiceUnavailable, "容器镜像仓库连接测试失败", "请检查仓库地址、网络连通性和仓库服务状态后重试。")
	}
}

func containerOperationMessage(c *gin.Context) string {
	switch c.FullPath() {
	case "/v1/containers/runtime":
		return "读取 Docker 运行时状态失败"
	case "/v1/containers/runtime/actions":
		return "执行 Docker 运行时服务操作失败"
	case "/v1/containers":
		if c.Request.Method == http.MethodPost {
			return "创建容器失败"
		}
		return "读取容器列表失败"
	case "/v1/containers/cleanup":
		return "清理容器资源失败"
	case "/v1/containers/tasks":
		return "读取容器任务列表失败"
	case "/v1/containers/tasks/:id":
		return "读取容器任务详情失败"
	case "/v1/containers/tasks/:id/events":
		return "订阅容器任务事件失败"
	case "/v1/containers/tasks/:id/log":
		return "读取容器任务日志失败"
	case "/v1/containers/tasks/:id/log/download":
		return "下载容器任务日志失败"
	case "/v1/containers/tasks/:id/cancel":
		return "取消容器任务失败"
	case "/v1/containers/:id":
		return "读取容器详情失败"
	case "/v1/containers/:id/stats":
		return "读取容器实时指标失败"
	case "/v1/containers/:id/actions":
		return "执行容器状态操作失败"
	case "/v1/containers/batch/actions":
		return "执行容器批量操作失败"
	case "/v1/containers/:id/logs":
		return "读取容器日志失败"
	case "/v1/containers/:id/logs/download":
		return "下载容器日志失败"
	case "/v1/containers/images":
		return "读取容器镜像列表失败"
	case "/v1/containers/images/import":
		return "导入容器镜像失败"
	case "/v1/containers/images/build":
		return "构建容器镜像失败"
	case "/v1/containers/images/build-cache/prune":
		return "清理容器镜像构建缓存失败"
	case "/v1/containers/images/:id/tag":
		return "创建容器镜像标签失败"
	case "/v1/containers/images/push":
		return "推送容器镜像失败"
	case "/v1/containers/images/:id/export":
		return "导出容器镜像失败"
	case "/v1/containers/images/:id":
		if c.Request.Method == http.MethodDelete {
			return "删除容器镜像失败"
		}
		return "读取容器镜像详情失败"
	case "/v1/containers/images/pull":
		return "拉取容器镜像失败"
	case "/v1/containers/images/prune":
		return "清理未使用容器镜像失败"
	case "/v1/containers/networks":
		if c.Request.Method == http.MethodPost {
			return "创建容器网络失败"
		}
		return "读取容器网络列表失败"
	case "/v1/containers/networks/:id":
		if c.Request.Method == http.MethodDelete {
			return "删除容器网络失败"
		}
		return "读取容器网络详情失败"
	case "/v1/containers/networks/prune":
		return "清理未使用容器网络失败"
	case "/v1/containers/networks/batch/delete":
		return "批量删除容器网络失败"
	case "/v1/containers/volumes":
		if c.Request.Method == http.MethodPost {
			return "创建容器存储卷失败"
		}
		return "读取容器存储卷列表失败"
	case "/v1/containers/volumes/:id":
		if c.Request.Method == http.MethodDelete {
			return "删除容器存储卷失败"
		}
		return "读取容器存储卷详情失败"
	case "/v1/containers/volumes/prune":
		return "清理未使用容器存储卷失败"
	case "/v1/containers/volumes/batch/delete":
		return "批量删除容器存储卷失败"
	case "/v1/containers/registries":
		if c.Request.Method == http.MethodPost {
			return "创建容器镜像仓库失败"
		}
		return "读取容器镜像仓库列表失败"
	case "/v1/containers/registries/:id":
		if c.Request.Method == http.MethodDelete {
			return "删除容器镜像仓库失败"
		}
		return "更新容器镜像仓库失败"
	case "/v1/containers/registries/:id/test":
		return "测试容器镜像仓库连接失败"
	case "/v1/containers/compose":
		return "读取 Compose 项目列表失败"
	case "/v1/containers/templates":
		if c.Request.Method == http.MethodPost {
			return "创建 Compose 模板失败"
		}
		return "读取 Compose 模板列表失败"
	case "/v1/containers/templates/:id":
		switch c.Request.Method {
		case http.MethodPut:
			return "更新 Compose 模板失败"
		case http.MethodDelete:
			return "删除 Compose 模板失败"
		default:
			return "读取 Compose 模板详情失败"
		}
	case "/v1/containers/config":
		if c.Request.Method == http.MethodGet {
			return "读取容器运行配置失败"
		}
		return "保存容器运行配置失败"
	default:
		return "处理容器管理请求失败"
	}
}

func recordAction(c *gin.Context, action string, status int, err error) {
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	username, _ := c.Get(middleware.ContextUsername)
	authMode, _ := c.Get(middleware.ContextAuthMode)
	outcome, message := "success", ""
	if err != nil {
		outcome, message = "failure", strings.TrimSpace(err.Error())
	}
	_, _ = manager.Append(auditservice.EventInput{
		RequestID: value(c, middleware.ContextRequestID), EventType: "container", Action: action,
		Method: c.Request.Method, Route: c.FullPath(), Path: c.Request.URL.Path, Status: status,
		Outcome: outcome, Sensitive: strings.Contains(action, "delete") || strings.Contains(action, "terminal"),
		UserID: userID, Username: valueFromAny(username), AuthMode: valueFromAny(authMode),
		RemoteIP: auditservice.RemoteIP(c.Request), UserAgent: c.GetHeader("User-Agent"), Message: message,
	})
}

func value(c *gin.Context, key string) string {
	v, _ := c.Get(key)
	return valueFromAny(v)
}

func valueFromAny(value any) string {
	result, _ := value.(string)
	return result
}
