package website

import (
	"encoding/json"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	approvalservice "oneinstack/internal/services/approval"
	certificateService "oneinstack/internal/services/certificate"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

const (
	ApprovalActionWebsiteDelete      = "website.delete"
	ApprovalActionWebsiteRestore     = "website.restore"
	ApprovalActionCertificateIssue   = "certificate.issue"
	ApprovalActionCertificateRenew   = "certificate.renew"
	ApprovalActionCertificateDisable = "certificate.disable"
)

type DeleteApprovalPayload struct {
	ID          int64  `json:"id"`
	DatabaseID  int64  `json:"databaseId"`
	DeleteFiles bool   `json:"deleteFiles"`
	ConfirmName string `json:"confirmName"`
}

type RestoreApprovalPayload struct {
	BackupID    string `json:"backupId"`
	ConfirmName string `json:"confirmName"`
}

type CertificateIssueApprovalPayload struct {
	WebsiteID       int64  `json:"websiteId"`
	Email           string `json:"email"`
	AutoRenew       *bool  `json:"autoRenew"`
	RenewBeforeDays int    `json:"renewBeforeDays"`
	ForceHTTPS      bool   `json:"forceHttps"`
}

type CertificateRenewApprovalPayload struct {
	CertificateID string `json:"certificateId"`
}

type CertificateDisableApprovalPayload struct {
	CertificateID string `json:"certificateId"`
	ConfirmDomain string `json:"confirmDomain"`
}

func shouldRequestWebsiteApproval(c *gin.Context) bool {
	access, ok := middleware.UserAccess(c)
	return ok && !access.IsSuperAdmin
}

func createWebsiteApproval(c *gin.Context, action, resourceName, resourceID string, payload interface{}) (*models.ApprovalRequest, error) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	username := ""
	if access != nil {
		username = access.Username
	}
	return approvalservice.NewService(app.DB()).Create(approvalservice.CreateInput{
		Module:          "website",
		Action:          action,
		ResourceID:      resourceID,
		ResourceName:    resourceName,
		RiskLevel:       "high",
		Reason:          action,
		Payload:         payload,
		RequestedBy:     userID,
		RequestedByName: username,
	})
}

func createOrReuseWebsiteApproval(c *gin.Context, action, resourceName, resourceID string, payload interface{}) (*models.ApprovalRequest, bool, error) {
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	username := ""
	if access != nil {
		username = access.Username
	}
	return approvalservice.NewService(app.DB()).CreateOrReusePending(approvalservice.CreateInput{
		Module:          "website",
		Action:          action,
		ResourceID:      resourceID,
		ResourceName:    resourceName,
		RiskLevel:       "high",
		Reason:          action,
		Payload:         payload,
		RequestedBy:     userID,
		RequestedByName: username,
	})
}

func canAccessWebsiteTask(c *gin.Context, requestedBy int64) bool {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		return false
	}
	access, _ := middleware.UserAccess(c)
	return canReadAllWebsiteTasks(access) || userID == requestedBy
}

func canReadAllWebsiteTasks(access *accessservice.UserAccess) bool {
	return access != nil &&
		(access.IsSuperAdmin || access.HasPermission(accessservice.PermissionTaskReadAll))
}

func ExecuteDeleteApproval(payload DeleteApprovalPayload, requestedBy int64) (*models.WebsiteTask, error) {
	manager, err := DefaultWebsiteTaskManager()
	if err != nil {
		return nil, err
	}
	return manager.SubmitDelete(payload.ID, payload.DatabaseID, payload.DeleteFiles, payload.ConfirmName, requestedBy)
}

func ExecuteRestoreApproval(payload RestoreApprovalPayload, requestedBy int64) (*models.WebsiteTask, error) {
	manager, err := getWebsiteTaskManager()
	if err != nil {
		return nil, err
	}
	return manager.SubmitRestore(payload.BackupID, payload.ConfirmName, requestedBy)
}

func ExecuteCertificateApproval(service *approvalservice.Service, request *models.ApprovalRequest) error {
	manager, err := getCertificateManager()
	if err != nil {
		return err
	}
	switch request.Action {
	case ApprovalActionCertificateIssue:
		var payload CertificateIssueApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		autoRenew := true
		if payload.AutoRenew != nil {
			autoRenew = *payload.AutoRenew
		}
		task, err := manager.SubmitIssue(certificateService.IssueOptions{
			WebsiteID:       payload.WebsiteID,
			Email:           payload.Email,
			AutoRenew:       autoRenew,
			RenewBeforeDays: payload.RenewBeforeDays,
			ForceHTTPS:      payload.ForceHTTPS,
			RequestedBy:     request.RequestedBy,
		})
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusExecuting, "certificate_task", task.ID, task)
	case ApprovalActionCertificateRenew:
		var payload CertificateRenewApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		task, err := manager.SubmitRenew(payload.CertificateID, request.RequestedBy)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusExecuting, "certificate_task", task.ID, task)
	case ApprovalActionCertificateDisable:
		var payload CertificateDisableApprovalPayload
		if err := json.Unmarshal([]byte(request.PayloadSnapshot), &payload); err != nil {
			return err
		}
		var certificate models.Certificate
		if err := app.DB().First(&certificate, "id = ?", payload.CertificateID).Error; err != nil {
			return err
		}
		updated, err := manager.Disable(certificate.WebsiteID)
		if err != nil {
			return err
		}
		return service.UpdateExecutionResult(request.ID, models.ApprovalStatusCompleted, "", "", updated)
	default:
		return core.NewError(core.ErrBadRequest, "不支持的证书审批动作")
	}
}
