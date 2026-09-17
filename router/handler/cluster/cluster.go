package cluster

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/services/cluster"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func manager(c *gin.Context) (*cluster.Manager, bool) {
	if cluster.GetAgentSettings().Role != cluster.ClusterRoleController {
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "本机未配置为集群控制端"))
		return nil, false
	}
	m, err := cluster.NewManager(app.DB())
	if err != nil {
		core.HandleErrorWithStatus(c, http.StatusInternalServerError, core.NewError(core.ErrInternalError, err.Error()))
		return nil, false
	}
	return m, true
}

func GetAgentSettings(c *gin.Context) {
	core.HandleSuccess(c, cluster.GetAgentSettings())
}

func SelectRole(c *gin.Context) {
	var request cluster.SelectClusterRoleInput
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "集群角色参数无效"))
		return
	}
	settings, err := cluster.SelectClusterRole(request)
	if errors.Is(err, cluster.ErrClusterRoleAlreadySelected) {
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "集群角色已选择，请先重置角色"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "集群角色只能选择控制端或节点端"))
		return
	}
	core.HandleSuccess(c, settings)
}

func ResetRole(c *gin.Context) {
	settings, err := cluster.ResetClusterRole()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "重置集群角色失败"))
		return
	}
	core.HandleSuccess(c, settings)
}

func UpdateAgentSettings(c *gin.Context) {
	var request cluster.UpdateAgentSettingsInput
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点模式配置参数无效"))
		return
	}
	settings, err := cluster.UpdateAgentSettings(request)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	core.HandleSuccess(c, settings)
}

func ListNodes(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	nodes, err := m.ListNodes()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	controller, _ := cluster.CollectLocalController(c.Request.Context())
	core.HandleSuccess(c, gin.H{"controller": controller, "items": nodes})
}

func GetNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	node, err := m.GetNode(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, node)
}

func ListMetrics(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	if _, err := m.GetNode(id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		} else {
			core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		}
		return
	}
	var since time.Time
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, "since 必须是 RFC3339 时间"))
			return
		}
		since = parsed
	}
	metrics, err := m.ListMetrics(id, since, 200)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, gin.H{"items": metrics})
}

func ListTasks(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	if strings.TrimSpace(c.Query("page")) == "" && strings.TrimSpace(c.Query("pageSize")) == "" {
		tasks, err := m.ListTasks(id, 100)
		if err != nil {
			core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
			return
		}
		core.HandleSuccess(c, gin.H{"items": tasks})
		return
	}
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "page 必须是正整数", "page"))
		return
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "pageSize 必须是 1 到 100 之间的整数", "pageSize"))
		return
	}
	tasks, err := m.ListTasksPage(id, page, pageSize)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, tasks)
}

func GetTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	taskID, err := strconv.ParseUint(strings.TrimSpace(c.Param("taskId")), 10, 64)
	if err != nil || taskID == 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidID, "任务 ID 必须是正整数", "taskId"))
		return
	}
	detail, err := m.GetTaskDetail(id, taskID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点任务不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取节点任务详情失败"))
		return
	}
	core.HandleSuccess(c, detail)
}

func EnqueueTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.EnqueueTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	task, err := m.EnqueueTask(input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	core.HandleSuccess(c, cluster.SummarizeTask(task))
}

func RestartNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	task, err := m.RestartPanel(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if errors.Is(err, cluster.ErrNodeUnavailable) {
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "仅在线且心跳正常的节点可以重启 Panel"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "创建节点重启任务失败"))
		return
	}
	core.HandleSuccess(c, task)
}

func DispatchWebsite(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.WebsiteDispatchInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	result, err := m.DispatchWebsite(input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "网站不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	core.HandleSuccess(c, result)
}

func CreateNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.CreateNodeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	result, err := m.CreateNode(input)
	if err != nil {
		handleNodeMutationError(c, err)
		return
	}
	core.HandleSuccess(c, result)
}

func UpdateNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	var input cluster.UpdateNodeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	node, err := m.UpdateNode(id, input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		handleNodeMutationError(c, err)
		return
	}
	core.HandleSuccess(c, node)
}

func handleNodeMutationError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cluster.ErrNameRequired):
		core.HandleValidationErrors(c, core.ValidationErrors{{Field: "name", Code: core.ErrRequiredField, Message: "节点名称不能为空"}})
	case errors.Is(err, cluster.ErrEndpointInvalid):
		core.HandleValidationErrors(c, core.ValidationErrors{{Field: "endpoint", Code: core.ErrInvalidParameter, Message: "Panel 地址必须是有效的 HTTP 或 HTTPS URL"}})
	case errors.Is(err, cluster.ErrNodeFieldTooLong):
		core.HandleValidationErrors(c, core.ValidationErrors{{Field: "node", Code: core.ErrInvalidParameter, Message: "节点名称、分组或标签长度超过限制"}})
	default:
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点参数无效"))
	}
}

func DeleteNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	err := m.DeleteNode(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": true})
}

func RotateToken(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	result, err := m.RotateToken(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, result)
}

func RegisterNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.NodeRegistration
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleErrorWithStatus(c, http.StatusBadRequest, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	input.Token = agentToken(c, input.Token)
	node, err := m.RegisterNode(input)
	if err != nil {
		agentError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"nodeId": node.ID, "status": node.Status})
}

func Heartbeat(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.NodeHeartbeat
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleErrorWithStatus(c, http.StatusBadRequest, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	input.Token = agentToken(c, input.Token)
	node, err := m.Heartbeat(input)
	if err != nil {
		agentError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"nodeId": node.ID, "status": node.Status, "lastSeenAt": node.LastSeenAt})
}

func ClaimTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	task, err := m.ClaimTask(agentToken(c, ""))
	if err != nil {
		agentError(c, err)
		return
	}
	if task == nil {
		core.HandleSuccess(c, gin.H{"task": nil})
		return
	}
	core.HandleSuccess(c, gin.H{"task": task})
}

func CompleteTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.TaskCompletion
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleErrorWithStatus(c, http.StatusBadRequest, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	input.Token = agentToken(c, input.Token)
	task, err := m.CompleteTask(input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleErrorWithStatus(c, http.StatusNotFound, core.NewError(core.ErrNotFound, "任务不存在"))
		return
	}
	if errors.Is(err, cluster.ErrTaskState) {
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "任务状态无效"))
		return
	}
	if err != nil {
		agentError(c, err)
		return
	}
	core.HandleSuccess(c, cluster.SummarizeTask(task))
}

func agentToken(c *gin.Context, bodyToken string) string {
	if strings.TrimSpace(bodyToken) != "" {
		return bodyToken
	}
	const prefix = "Bearer "
	authorization := strings.TrimSpace(c.GetHeader("Authorization"))
	if strings.HasPrefix(authorization, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	}
	return ""
}

func nodeID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || id == 0 {
		core.HandleError(c, core.NewError(core.ErrInvalidID, "节点 ID 无效"))
		return 0, false
	}
	return uint(id), true
}

func agentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, cluster.ErrInvalidToken):
		core.HandleErrorWithStatus(c, http.StatusUnauthorized, core.NewError(core.ErrUnauthorized, "节点令牌无效"))
	case errors.Is(err, cluster.ErrNodeDisabled):
		core.HandleErrorWithStatus(c, http.StatusForbidden, core.NewError(core.ErrForbidden, "节点已被禁用"))
	default:
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
	}
}
