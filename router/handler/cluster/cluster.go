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
	m, err := cluster.NewManager(app.DB())
	if err != nil {
		core.HandleErrorWithStatus(c, http.StatusInternalServerError, core.NewError(core.ErrInternalError, err.Error()))
		return nil, false
	}
	return m, true
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
	core.HandleSuccess(c, gin.H{"items": nodes})
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
	tasks, err := m.ListTasks(id, 100)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, err.Error()))
		return
	}
	core.HandleSuccess(c, gin.H{"items": tasks})
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
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
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
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	core.HandleSuccess(c, node)
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
	core.HandleSuccess(c, task)
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
