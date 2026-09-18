package cluster

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"oneinstack/core"
	auditservice "oneinstack/internal/services/audit"
	clusterservice "oneinstack/internal/services/cluster"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func GetPolicy(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	policy, err := m.GetPolicy()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取集群策略失败"))
		return
	}
	core.HandleSuccess(c, policy)
}

func UpdatePolicy(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input clusterservice.PolicyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "集群策略参数无效"))
		return
	}
	policy, err := m.UpdatePolicy(input)
	if err != nil {
		recordClusterAudit(c, "cluster.policy.update", http.StatusBadRequest, "result=failed reason=invalid_policy")
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	recordClusterAudit(c, "cluster.policy.update", http.StatusOK, fmt.Sprintf("policy=%d", policy.ID))
	core.HandleSuccess(c, policy)
}

func DiagnoseNode(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := m.EnqueueDiagnosis(id, "", userID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		recordClusterAudit(c, "cluster.node.diagnose", http.StatusNotFound, fmt.Sprintf("node=%d result=failed reason=not_found", id))
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if errors.Is(err, clusterservice.ErrNodeLifecycle) {
		recordClusterAudit(c, "cluster.node.diagnose", http.StatusConflict, fmt.Sprintf("node=%d result=failed reason=lifecycle", id))
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点正在排空，暂不接受新的诊断任务"))
		return
	}
	if err != nil && !errors.Is(err, clusterservice.ErrDiagnosisCapability) {
		log.Printf("cluster diagnosis enqueue failed node=%d: %v", id, err)
		recordClusterAudit(c, "cluster.node.diagnose", http.StatusInternalServerError, fmt.Sprintf("node=%d result=failed reason=enqueue_failed", id))
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "创建节点诊断任务失败"))
		return
	}
	recordClusterAudit(c, "cluster.node.diagnose", http.StatusAccepted, fmt.Sprintf("node=%d task=%d", id, task.ID))
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, clusterservice.SummarizeTask(task)))
}

type lifecycleRequest struct {
	Action string `json:"action"`
}

func ChangeNodeLifecycle(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	var input lifecycleRequest
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "生命周期操作参数无效"))
		return
	}
	auditAction := clusterLifecycleAuditAction(input.Action)
	node, err := m.ApplyLifecycle(id, input.Action)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		recordClusterAudit(c, auditAction, http.StatusNotFound, fmt.Sprintf("node=%d result=failed reason=not_found", id))
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点不存在"))
		return
	}
	if errors.Is(err, clusterservice.ErrNodeLifecycle) {
		recordClusterAudit(c, auditAction, http.StatusConflict, fmt.Sprintf("node=%d result=failed reason=invalid_transition", id))
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "当前节点状态不允许执行该生命周期操作"))
		return
	}
	if err != nil {
		recordClusterAudit(c, auditAction, http.StatusInternalServerError, fmt.Sprintf("node=%d result=failed reason=internal", id))
		core.HandleError(c, core.NewError(core.ErrInternalError, "修改节点生命周期状态失败"))
		return
	}
	recordClusterAudit(c, auditAction, http.StatusOK, fmt.Sprintf("node=%d lifecycle=%s", id, node.LifecycleStatus))
	core.HandleSuccess(c, node)
}

func PreviewBatch(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input clusterservice.BatchPreviewInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "批量操作参数无效"))
		return
	}
	preview, err := m.PreviewBatch(input)
	if err != nil {
		recordClusterAudit(c, "cluster.batch.preview", http.StatusBadRequest, fmt.Sprintf("action=%s result=failed", boundedAuditMessage(input.Action)))
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	recordClusterAudit(c, "cluster.batch.preview", http.StatusOK, fmt.Sprintf("preview=%s action=%s executable=%d blocked=%d skipped=%d", preview.ID, preview.Action, len(preview.Executable), len(preview.Blocked), len(preview.Skipped)))
	core.HandleSuccess(c, preview)
}

func CreateBatch(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input clusterservice.ExecuteBatchInput
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "批次执行参数无效"))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	batch, err := m.ExecuteBatch(input, userID)
	switch {
	case errors.Is(err, clusterservice.ErrBatchPreview):
		recordClusterAudit(c, "cluster.batch.execute", http.StatusGone, fmt.Sprintf("preview=%s result=failed reason=expired", boundedAuditMessage(input.PreviewID)))
		core.HandleErrorWithStatus(c, http.StatusGone, core.NewError(core.ErrConflict, "批次预览不存在或已过期，请重新预览"))
		return
	case errors.Is(err, clusterservice.ErrBatchPreviewStale):
		recordClusterAudit(c, "cluster.batch.execute", http.StatusConflict, fmt.Sprintf("preview=%s result=failed reason=stale", boundedAuditMessage(input.PreviewID)))
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "节点状态已变化，请重新预览"))
		return
	case errors.Is(err, clusterservice.ErrBatchConfirm):
		recordClusterAudit(c, "cluster.batch.execute", http.StatusBadRequest, fmt.Sprintf("preview=%s result=failed reason=confirmation", boundedAuditMessage(input.PreviewID)))
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "危险操作确认文本错误"))
		return
	case err != nil:
		recordClusterAudit(c, "cluster.batch.execute", http.StatusBadRequest, fmt.Sprintf("preview=%s result=failed reason=invalid", boundedAuditMessage(input.PreviewID)))
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, err.Error()))
		return
	}
	recordClusterAudit(c, "cluster.batch."+batch.Action, http.StatusAccepted, fmt.Sprintf("batch=%s total=%d", batch.ID, batch.Total))
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, batch))
}

func ListBatches(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	page, pageSize, valid := pagination(c)
	if !valid {
		return
	}
	result, err := m.ListBatches(page, pageSize)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取批次列表失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetBatch(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	result, err := m.GetBatch(c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "批次不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取批次详情失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func CancelBatch(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	batch, err := m.CancelBatch(c.Param("id"))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		recordClusterAudit(c, "cluster.batch.cancel", http.StatusNotFound, fmt.Sprintf("batch=%s result=failed reason=not_found", boundedAuditMessage(c.Param("id"))))
		core.HandleError(c, core.NewError(core.ErrNotFound, "批次不存在"))
		return
	}
	if err != nil {
		recordClusterAudit(c, "cluster.batch.cancel", http.StatusConflict, fmt.Sprintf("batch=%s result=failed reason=non_interruptible", boundedAuditMessage(c.Param("id"))))
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "批次包含不可取消的执行阶段"))
		return
	}
	recordClusterAudit(c, "cluster.batch.cancel", http.StatusOK, fmt.Sprintf("batch=%s canceled=%d", batch.ID, batch.Canceled))
	core.HandleSuccess(c, batch)
}

func ListAllTasks(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	page, pageSize, valid := pagination(c)
	if !valid {
		return
	}
	var nodeIDValue uint
	if raw := strings.TrimSpace(c.Query("nodeId")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 32)
		if err != nil || parsed == 0 {
			core.HandleError(c, core.NewError(core.ErrInvalidParameter, "nodeId 必须是正整数"))
			return
		}
		nodeIDValue = uint(parsed)
	}
	result, err := m.ListAllTasks(clusterservice.TaskFilter{NodeID: nodeIDValue, BatchID: c.Query("batchId"), Type: c.Query("type"), Status: c.Query("status"), Page: page, PageSize: pageSize})
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取集群任务失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func CancelNodeTask(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	taskID, valid := taskID(c)
	if !valid {
		return
	}
	task, err := m.CancelTask(id, taskID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		recordClusterAudit(c, "cluster.task.cancel", http.StatusNotFound, fmt.Sprintf("node=%d task=%d result=failed reason=not_found", id, taskID))
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点任务不存在"))
		return
	}
	if errors.Is(err, clusterservice.ErrTaskState) {
		recordClusterAudit(c, "cluster.task.cancel", http.StatusConflict, fmt.Sprintf("node=%d task=%d result=failed reason=non_interruptible", id, taskID))
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrConflict, "任务处于不可中断阶段，无法取消"))
		return
	}
	if err != nil {
		recordClusterAudit(c, "cluster.task.cancel", http.StatusInternalServerError, fmt.Sprintf("node=%d task=%d result=failed reason=internal", id, taskID))
		core.HandleError(c, core.NewError(core.ErrInternalError, "取消任务失败"))
		return
	}
	recordClusterAudit(c, "cluster.task.cancel", http.StatusOK, fmt.Sprintf("node=%d task=%d", id, taskID))
	core.HandleSuccess(c, task)
}

func ListTaskEvents(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	id, ok := nodeID(c)
	if !ok {
		return
	}
	taskID, valid := taskID(c)
	if !valid {
		return
	}
	events, err := m.ListTaskEvents(id, taskID, 500)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "节点任务不存在"))
		return
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取任务事件失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"items": events})
}

func TaskControl(c *gin.Context) {
	m, ok := manager(c)
	if !ok {
		return
	}
	var input struct {
		TaskID uint64 `json:"taskId"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || input.TaskID == 0 {
		core.HandleErrorWithStatus(c, http.StatusBadRequest, core.NewError(core.ErrInvalidParameter, "任务 ID 无效"))
		return
	}
	control, err := m.TaskControl(agentToken(c, ""), input.TaskID)
	if err != nil {
		agentError(c, err)
		return
	}
	core.HandleSuccess(c, control)
}

func pagination(c *gin.Context) (int, int, bool) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "page 必须是正整数"))
		return 0, 0, false
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		core.HandleError(c, core.NewError(core.ErrInvalidParameter, "pageSize 必须是 1 到 100 之间的整数"))
		return 0, 0, false
	}
	return page, pageSize, true
}

func taskID(c *gin.Context) (uint64, bool) {
	value, err := strconv.ParseUint(strings.TrimSpace(c.Param("taskId")), 10, 64)
	if err != nil || value == 0 {
		core.HandleError(c, core.NewError(core.ErrInvalidID, "任务 ID 无效"))
		return 0, false
	}
	return value, true
}

func recordClusterAudit(c *gin.Context, action string, status int, message string) {
	manager := auditservice.Default()
	if manager == nil {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	username := ""
	if access, ok := middleware.UserAccess(c); ok && access != nil {
		username = access.Username
	}
	requestID, _ := c.Get(middleware.ContextRequestID)
	requestIDText, _ := requestID.(string)
	_, _ = manager.Append(auditservice.EventInput{
		RequestID: requestIDText, EventType: "cluster", Action: action,
		Method: c.Request.Method, Route: c.FullPath(), Path: c.Request.URL.Path,
		Status: status, Outcome: auditservice.OutcomeForStatus(status), Sensitive: true,
		UserID: userID, Username: username, RemoteIP: auditservice.RemoteIP(c.Request),
		UserAgent: c.GetHeader("User-Agent"), ContentLength: c.Request.ContentLength,
		Message: boundedAuditMessage(message), CreatedAt: time.Now().UTC(),
	})
}

func boundedAuditMessage(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}

func clusterLifecycleAuditAction(action string) string {
	action = strings.TrimSpace(action)
	switch action {
	case clusterservice.LifecycleEnterMaintenance, clusterservice.LifecycleExitMaintenance,
		clusterservice.LifecycleDrain, clusterservice.LifecycleResume,
		clusterservice.LifecycleDisable, clusterservice.LifecycleEnable,
		clusterservice.LifecycleMarkDelete, clusterservice.LifecycleRestoreDelete:
		return "cluster.node.lifecycle." + action
	default:
		return "cluster.node.lifecycle.invalid"
	}
}
