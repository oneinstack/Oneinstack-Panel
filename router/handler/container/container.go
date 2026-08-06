package container

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
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
	core.HandleSuccess(c, service.Runtime(ctx))
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
		operationError(c, err)
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
		if containers, ok := item["Containers"].(float64); ok {
			item["used"] = containers > 0
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
			item["success"], item["error"] = false, err.Error()
			recordAction(c, "container.network.delete", http.StatusInternalServerError, err)
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
			item["success"], item["error"] = false, err.Error()
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
	ctx, cancel := requestContext(c)
	defer cancel()
	ports := make([]containerService.PortMapping, 0, len(request.Ports))
	for _, port := range request.Ports {
		ports = append(ports, containerService.PortMapping{HostPort: port.HostPort, ContainerPort: port.ContainerPort, Protocol: port.Protocol})
	}
	mounts := make([]containerService.Mount, 0, len(request.Mounts))
	for _, mount := range request.Mounts {
		mounts = append(mounts, containerService.Mount{Source: mount.Source, Target: mount.Target, ReadOnly: mount.ReadOnly})
	}
	createRequest := containerService.ContainerCreateRequest{
		Name: request.Name, Image: request.Image, Ports: ports, Networks: request.Networks, IPv4: request.IPv4, IPv6: request.IPv6,
		Mounts: mounts, Command: request.Command, Entrypoint: request.Entrypoint, AutoRemove: request.AutoRemove, Privileged: request.Privileged,
		TTY: request.TTY, OpenStdin: request.OpenStdin, Restart: request.Restart, CPUWeight: request.CPUWeight, CPULimit: request.CPULimit,
		MemoryLimitMB: request.MemoryLimitMB, Labels: request.Labels, Environment: request.Environment,
	}
	available, err := service.ImageAvailable(ctx, request.Image)
	if err != nil {
		recordAction(c, "container.create", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	if !available {
		userID, _ := middleware.AuthenticatedUserID(c)
		task, taskErr := createTaskManager.Submit(createRequest, userID)
		if taskErr != nil {
			recordAction(c, "container.create", http.StatusInternalServerError, taskErr)
			operationError(c, taskErr)
			return
		}
		recordAction(c, "container.create", http.StatusAccepted, nil)
		c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{
			"action":      "container.create",
			"taskId":      task.ID,
			"status":      task.Status,
			"containerId": nil,
			"name":        task.Name,
			"image":       task.Image,
		}))
		return
	}
	id, err := service.CreateContainer(ctx, createRequest)
	if err != nil {
		recordAction(c, "container.create", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.create", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": strings.TrimSpace(id), "action": "container.create"})
}

func GetContainerCreateTask(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := createTaskManager.Get(c.Param("taskId"), userID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "容器创建任务不存在"))
		return
	}
	core.HandleSuccess(c, task)
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
			item["error"] = err.Error()
			recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusInternalServerError, err)
		} else {
			item["success"] = true
			recordAction(c, "container.batch."+strings.ToLower(request.Action), http.StatusOK, nil)
		}
		items = append(items, item)
	}
	core.HandleSuccess(c, gin.H{"items": items, "total": len(items)})
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
	recordAction(c, "container."+strings.ToLower(request.Action), http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "action": request.Action})
}

func Logs(c *gin.Context) {
	tail, _ := strconv.Atoi(c.DefaultQuery("tail", "500"))
	ctx, cancel := requestContext(c)
	defer cancel()
	result, err := service.Logs(ctx, c.Param("id"), tail)
	if err != nil {
		recordAction(c, "container.logs.read", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"id": c.Param("id"), "logs": result})
}

func PullImage(c *gin.Context) {
	var request input.ContainerImagePullRequest
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
	if err := service.PullImage(ctx, reference); err != nil {
		recordAction(c, "container.image.pull", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.pull", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{"action": "container.image.pull", "reference": reference}))
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
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{"action": "container.image.import"}))
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
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{"action": "container.image.push", "reference": reference}))
}

func BuildImage(c *gin.Context) {
	var request input.ContainerImageBuildRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	if err := service.BuildImage(ctx, request.Name, request.Dockerfile, request.ContextPath, request.DockerfilePath, request.Labels, request.LabelsText); err != nil {
		recordAction(c, "container.image.build", http.StatusInternalServerError, err)
		operationError(c, err)
		return
	}
	recordAction(c, "container.image.build", http.StatusAccepted, nil)
	c.JSON(http.StatusAccepted, core.SuccessResponse(gin.H{"action": "container.image.build", "name": request.Name}))
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
	core.HandleSuccess(c, gin.H{"items": items, "total": total, "page": page, "pageSize": pageSize})
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
		recordAction(c, "container.registry.test", http.StatusInternalServerError, err)
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
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
}

func operationError(c *gin.Context, err error) {
	if errors.Is(err, containerService.ErrInvalidContainerConfig) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrBadRequest,
			"容器配置无效",
			strings.TrimPrefix(err.Error(), containerService.ErrInvalidContainerConfig.Error()+": "),
		))
		return
	}
	if errors.Is(err, containerService.ErrRuntimeUnavailable) {
		core.HandleError(c, core.NewErrorWithDetail(
			core.ErrContainerRuntimeUnavailable,
			"Docker运行时不可用",
			err.Error(),
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
	core.HandleError(c, core.WrapError(err, core.ErrInternalError, "Docker操作失败"))
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
