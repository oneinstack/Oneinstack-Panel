package approval

import (
	"encoding/json"
	"errors"
	"strconv"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	approvalservice "oneinstack/internal/services/approval"
	"oneinstack/router/handler/storage"
	"oneinstack/router/handler/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func List(c *gin.Context) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	result, err := approvalservice.NewService(app.DB()).List(approvalservice.ListOptions{
		Page:       positiveQueryInt(c, "page", 1),
		PageSize:   positiveQueryInt(c, "pageSize", 20),
		Status:     c.Query("status"),
		Module:     c.Query("module"),
		Mine:       c.Query("mine") == "true",
		Keyword:    c.Query("keyword"),
		UserID:     userID,
		IncludeAll: access != nil && (access.IsSuperAdmin || access.HasPermission(accessservice.PermissionApprovalRead)),
	})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取审批单失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func Get(c *gin.Context) {
	request, err := approvalservice.NewService(app.DB()).Get(c.Param("id"))
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, "审批单不存在"))
		return
	}
	if !canViewApproval(c, request) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该审批单"))
		return
	}
	response := gin.H{
		"id":              request.ID,
		"module":          request.Module,
		"action":          request.Action,
		"resourceId":      request.ResourceID,
		"resourceName":    request.ResourceName,
		"riskLevel":       request.RiskLevel,
		"status":          request.Status,
		"reason":          request.Reason,
		"requestedBy":     request.RequestedBy,
		"requestedByName": request.RequestedByName,
		"approvedBy":      request.ApprovedBy,
		"approvedByName":  request.ApprovedByName,
		"reviewComment":   request.ReviewComment,
		"boundTaskType":   request.BoundTaskType,
		"boundTaskId":     request.BoundTaskID,
		"createdAt":       request.CreatedAt,
		"updatedAt":       request.UpdatedAt,
		"expiresAt":       request.ExpiresAt,
	}
	var payload any
	if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err == nil {
		response["payloadSnapshot"] = payload
	}
	if request.ResultPayload != "" && canViewApprovalResult(c, request) {
		var result any
		if err := json.Unmarshal([]byte(request.ResultPayload), &result); err == nil {
			response["result"] = result
		}
	}
	core.HandleSuccess(c, response)
}

func Approve(c *gin.Context) {
	var review input.ApprovalReviewRequest
	if err := c.ShouldBindJSON(&review); err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "审批通过参数格式不正确", "请求体必须为 JSON 对象；不填写审批意见时请传入 {}。"))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	if access == nil || (!access.IsSuperAdmin && !access.HasPermission(accessservice.PermissionApprovalReview)) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权审批"))
		return
	}
	request, err := approvalservice.NewService(app.DB()).Approve(c.Param("id"), userID, access.Username, review.Comment)
	if err != nil {
		handleApprovalReviewError(c, err, "审批通过")
		return
	}
	if err := executeApprovedRequest(c, request); err != nil {
		_ = approvalservice.NewService(app.DB()).UpdateExecutionResult(request.ID, models.ApprovalStatusFailed, "", "", gin.H{"error": err.Error()})
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "执行审批请求失败"))
		return
	}
	updated, _ := approvalservice.NewService(app.DB()).Get(request.ID)
	core.HandleSuccess(c, updated)
}

func Reject(c *gin.Context) {
	var review input.ApprovalReviewRequest
	if err := c.ShouldBindJSON(&review); err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, "审批拒绝参数格式不正确", "请求体必须为 JSON 对象；不填写审批意见时请传入 {}。"))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	if access == nil || (!access.IsSuperAdmin && !access.HasPermission(accessservice.PermissionApprovalReview)) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权审批"))
		return
	}
	request, err := approvalservice.NewService(app.DB()).Reject(c.Param("id"), userID, access.Username, review.Comment)
	if err != nil {
		handleApprovalReviewError(c, err, "审批拒绝")
		return
	}
	core.HandleSuccess(c, request)
}

func handleApprovalReviewError(c *gin.Context, err error, action string) {
	switch {
	case errors.Is(err, approvalservice.ErrRequesterCannotReview):
		core.HandleError(c, core.NewError(core.ErrApprovalSelfReview, "申请人不能审批自己的申请"))
	case errors.Is(err, approvalservice.ErrApprovalExpired):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrOperationExpired, "审批单已过期，请重新发起申请", "该审批单已超过有效期，无法继续处理。请重新发起审批申请。"))
	case errors.Is(err, approvalservice.ErrApprovalNotPending):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrResourceStateInvalid, "审批单当前不是待审批状态，请刷新后重试", "该审批单可能已被其他人员处理。请刷新页面以获取最新状态。"))
	case errors.Is(err, gorm.ErrRecordNotFound):
		core.HandleError(c, core.NewErrorWithDetail(core.ErrNotFound, "审批单不存在或已被删除", "请刷新审批列表后重新选择需要处理的审批单。"))
	default:
		core.HandleError(c, core.NewErrorWithDetail(core.ErrBadRequest, action+"失败", "审批处理未完成。请刷新审批单确认当前状态；如仍失败，请联系管理员检查服务日志。"))
	}
}

func canViewApproval(c *gin.Context, request *models.ApprovalRequest) bool {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	if access != nil && access.IsSuperAdmin {
		return true
	}
	if request.RequestedBy == userID {
		return true
	}
	return access != nil && access.HasPermission(accessservice.PermissionApprovalRead)
}

func canViewApprovalResult(c *gin.Context, request *models.ApprovalRequest) bool {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	return request.RequestedBy == userID || (access != nil && access.IsSuperAdmin)
}

func executeApprovedRequest(c *gin.Context, request *models.ApprovalRequest) error {
	service := approvalservice.NewService(app.DB())
	switch request.Action {
	case website.ApprovalActionWebsiteDelete:
		var payload website.DeleteApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		task, err := website.ExecuteDeleteApproval(payload, request.RequestedBy)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusExecuting, "website_task", task.ID, task)
	case website.ApprovalActionWebsiteRestore:
		var payload website.RestoreApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		task, err := website.ExecuteRestoreApproval(payload, request.RequestedBy)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusExecuting, "website_task", task.ID, task)
	case website.ApprovalActionCertificateIssue, website.ApprovalActionCertificateRenew, website.ApprovalActionCertificateDisable:
		return website.ExecuteCertificateApproval(service, request)
	case storage.ApprovalActionDatabaseRestore:
		var payload storage.RestoreApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		task, err := storage.ExecuteRestoreApproval(payload, request.RequestedBy)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusExecuting, "database_task", task.ID, task)
	case storage.ApprovalActionDatabaseCredentialReveal:
		var payload storage.RevealCredentialApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		result, err := storage.ExecuteRevealCredentialApproval(payload)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusCompleted, "", "", result)
	case storage.ApprovalActionDatabaseConnectionDelete:
		var payload storage.DeleteConnectionApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		if err := storage.ExecuteDeleteConnectionApproval(payload); err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusCompleted, "", "", gin.H{
			"connectionId": payload.ConnectionID,
			"deleted":      true,
		})
	default:
		return errors.New("unsupported approval action")
	}
}

func positiveQueryInt(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(name, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
