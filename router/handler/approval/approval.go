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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "审批通过参数格式不正确"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "审批通过失败"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "审批拒绝参数格式不正确"))
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
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "审批拒绝失败"))
		return
	}
	core.HandleSuccess(c, request)
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
