package cluster

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/services/cluster"
	"oneinstack/router/middleware"

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
	if policy, policyErr := m.GetPolicy(); policyErr == nil {
		cluster.ApplyControllerPolicy(&controller, policy)
	}
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
	input.RequestedBy, _ = middleware.AuthenticatedUserID(c)
	task, err := m.EnqueueTask(input)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	recordClusterAudit(c, "cluster.task.create", http.StatusOK, fmt.Sprintf("node=%d task=%d type=%s", task.NodeID, task.ID, task.Type))
	core.HandleSuccess(c, cluster.SummarizeTask(task))
}

func RestartNode(c *gin.Context) {
	_, ok := manager(c)
	if !ok {
		return
	}
	_, ok = nodeID(c)
	if !ok {
		return
	}
	core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点重启必须通过批次预览并输入 RESTART NODES 确认"))
}

func GetPanelUpdate(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	state, err := m.GetPanelUpdateState(id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取节点面板更新状态失败"))
		return
	}
	core.HandleSuccess(c, state)
}

func ListPanelUpdates(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	raw := strings.TrimSpace(c.Query("nodeIds"))
	if raw == "" {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点 ID 列表不能为空"))
		return
	}
	parts := strings.Split(raw, ",")
	if len(parts) > 100 {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "单次最多查询 100 个节点"))
		return
	}
	ids := make([]uint, 0, len(parts))
	seen := make(map[uint]struct{}, len(parts))
	for _, part := range parts {
		value, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		id := uint(value)
		if err != nil || id == 0 {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点 ID 列表格式无效"))
			return
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	states, err := m.GetPanelUpdateStates(ids)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取节点面板更新状态失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"items": states})
}

func CheckPanelUpdate(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	task, err := m.EnqueuePanelUpdateCheck(id)
	if handlePanelUpdateMutationError(c, err) {
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func ApplyPanelUpdate(c *gin.Context) {
	_, ok := manager(c)
	if !ok {
		return
	}
	_, ok = nodeID(c)
	if !ok {
		return
	}
	core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "执行更新必须通过批次预览并输入 UPDATE NODES 确认"))
}

func handlePanelUpdateMutationError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
	case errors.Is(err, cluster.ErrNodeUnavailable):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "仅在线、已启用且心跳正常的节点可以检查或执行更新"))
	case errors.Is(err, cluster.ErrPanelUpdateCapability):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点 Agent 版本不支持面板更新，请先在节点端升级 Panel"))
	case errors.Is(err, cluster.ErrPanelUpdateActive):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "该节点已有面板更新任务正在执行"))
	case errors.Is(err, cluster.ErrNodeLifecycle):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点正在排空，暂不接受新的更新任务"))
	case errors.Is(err, cluster.ErrPanelUpdateTarget):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "更新检查结果已失效，请重新检查可用版本"))
	case errors.Is(err, cluster.ErrPanelUpdateConfirm):
		core.HandleError(c, core.NewError(core.ErrBadRequest, "确认文本必须为 UPDATE PANEL"))
	default:
		core.HandleError(c, core.NewError(core.ErrInternalError, "创建节点面板更新任务失败"))
	}
	return true
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
	case errors.Is(err, cluster.ErrEndpointExists):
		message := "Panel 地址已存在，请勿重复添加节点"
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(core.ErrConflict, message, message))
	case errors.Is(err, cluster.ErrNodeFieldTooLong):
		core.HandleValidationErrors(c, core.ValidationErrors{{Field: "node", Code: core.ErrInvalidParameter, Message: "节点名称、分组或标签长度超过限制"}})
	case errors.Is(err, cluster.ErrNodeLifecycle):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "当前节点生命周期状态不允许此操作"))
	default:
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "节点参数无效"))
	}
}

func DeleteNode(c *gin.Context) {
	_, ok := manager(c)
	if !ok {
		return
	}
	_, ok = nodeID(c)
	if !ok {
		return
	}
	core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "最终删除必须通过批次预览并输入 DELETE NODES 确认"))
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
	if errors.Is(err, cluster.ErrNodeLifecycle) {
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "已禁用或待删除节点不能轮换令牌"))
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

func MarkOffline(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	node, err := m.MarkOffline(agentToken(c, ""))
	if err != nil {
		agentError(c, err)
		return
	}
	core.HandleSuccess(c, gin.H{"nodeId": node.ID, "status": node.Status})
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

func ProgressTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input cluster.TaskProgressInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleErrorWithStatus(c, http.StatusBadRequest, core.NewError(core.ErrInvalidParameter, "请求参数无效"))
		return
	}
	input.Token = agentToken(c, input.Token)
	task, err := m.ReportTaskProgress(input)
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
	case errors.Is(err, cluster.ErrNodeDeparted):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点已离开集群，请重新注册"))
	default:
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
	}
}
