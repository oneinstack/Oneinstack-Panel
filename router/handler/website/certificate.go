package website

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	certificateService "oneinstack/internal/services/certificate"
	websiteService "oneinstack/internal/services/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	certificateManagerMu sync.Mutex
	certificateManagerDB *gorm.DB
	certificateManager   *certificateService.Manager
)

func getCertificateManager() (*certificateService.Manager, error) {
	certificateManagerMu.Lock()
	defer certificateManagerMu.Unlock()
	database := app.DB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	if certificateManager == nil || certificateManagerDB != database {
		deployer, err := websiteService.NewCertificateDeployer()
		if err != nil {
			return nil, err
		}
		certificateManager = certificateService.NewManager(
			database,
			app.ONE_CONFIG.System.CertificatePath,
			app.ONE_CONFIG.System.ACMEChallengePath,
			app.ONE_CONFIG.System.ACMEDirectoryURL,
			time.Duration(app.ONE_CONFIG.System.ACMEIssueTimeoutMinutes)*time.Minute,
			&certificateService.ACMEIssuer{},
			deployer,
		)
		certificateManagerDB = database
	}
	if err := certificateManager.Start(); err != nil {
		return nil, err
	}
	return certificateManager, nil
}

// DefaultCertificateManager initializes interrupted-task recovery at startup.
func DefaultCertificateManager() (*certificateService.Manager, error) {
	return getCertificateManager()
}

func IssueCertificate(c *gin.Context) {
	var request input.CertificateIssueParam
	if err := c.ShouldBindJSON(&request); err != nil || request.WebsiteID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "证书签发参数不完整"))
		return
	}
	autoRenew := true
	if request.AutoRenew != nil {
		autoRenew = *request.AutoRenew
	}
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	if shouldRequestWebsiteApproval(c) {
		approval, err := createWebsiteApproval(c, ApprovalActionCertificateIssue, "", strconv.FormatInt(request.WebsiteID, 10), CertificateIssueApprovalPayload{
			WebsiteID:       request.WebsiteID,
			Email:           request.Email,
			AutoRenew:       request.AutoRenew,
			RenewBeforeDays: request.RenewBeforeDays,
			ForceHTTPS:      request.ForceHTTPS,
		})
		if err != nil {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建证书签发审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
		}))
		return
	}
	task, err := manager.SubmitIssue(certificateService.IssueOptions{
		WebsiteID:       request.WebsiteID,
		Email:           request.Email,
		AutoRenew:       autoRenew,
		RenewBeforeDays: request.RenewBeforeDays,
		ForceHTTPS:      request.ForceHTTPS,
		RequestedBy:     userID,
	})
	if err != nil {
		handleCertificateError(c, err, "创建证书签发任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func GetCertificate(c *gin.Context) {
	websiteID, err := strconv.ParseInt(c.Param("websiteId"), 10, 64)
	if err != nil || websiteID <= 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "websiteId 必须是正整数", "websiteId"))
		return
	}
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	certificate, err := manager.GetCertificateByWebsite(websiteID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleSuccess(c, nil)
		return
	}
	if err != nil {
		handleCertificateError(c, err, "读取网站证书失败")
		return
	}
	core.HandleSuccess(c, certificate)
}

func RenewCertificate(c *gin.Context) {
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	if shouldRequestWebsiteApproval(c) {
		approval, err := createWebsiteApproval(c, ApprovalActionCertificateRenew, "", c.Param("id"), CertificateRenewApprovalPayload{
			CertificateID: c.Param("id"),
		})
		if err != nil {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建证书续签审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
		}))
		return
	}
	task, err := manager.SubmitRenew(c.Param("id"), userID)
	if err != nil {
		handleCertificateError(c, err, "创建证书续签任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func DisableCertificate(c *gin.Context) {
	var request input.CertificateDisableParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入主域名确认关闭 SSL"))
		return
	}
	var certificate models.Certificate
	if err := app.DB().First(&certificate, "id = ?", c.Param("id")).Error; err != nil {
		handleCertificateError(c, err, "网站证书不存在")
		return
	}
	var website models.Website
	if err := app.DB().First(&website, "id = ?", certificate.WebsiteID).Error; err != nil {
		handleCertificateError(c, err, "网站不存在")
		return
	}
	if !strings.EqualFold(strings.TrimSpace(request.ConfirmDomain), website.Name) {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "主域名确认不匹配"))
		return
	}
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	if shouldRequestWebsiteApproval(c) {
		approval, err := createWebsiteApproval(c, ApprovalActionCertificateDisable, website.Name, c.Param("id"), CertificateDisableApprovalPayload{
			CertificateID: c.Param("id"),
			ConfirmDomain: request.ConfirmDomain,
		})
		if err != nil {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建证书禁用审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
		}))
		return
	}
	updated, err := manager.Disable(website.ID)
	if err != nil {
		handleCertificateError(c, err, "关闭网站 SSL 失败")
		return
	}
	core.HandleSuccess(c, updated)
}

func ListCertificateTasks(c *gin.Context) {
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	websiteID, _ := strconv.ParseInt(c.Query("websiteId"), 10, 64)
	result, err := manager.ListTasks(certificateService.TaskListOptions{
		WebsiteID: websiteID,
		Status:    c.Query("status"),
		Page:      positiveCertificateQueryInt(c, "page", 1),
		PageSize:  positiveCertificateQueryInt(c, "pageSize", 20),
	})
	if err != nil {
		handleCertificateError(c, err, "读取证书任务失败")
		return
	}
	core.HandleSuccess(c, result)
}

func GetCertificateTask(c *gin.Context) {
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.GetTask(c.Param("id"))
	if err != nil {
		handleCertificateError(c, err, "读取证书任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func GetCertificateTaskLog(c *gin.Context) {
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	result, err := manager.ReadTaskLog(c.Param("id"))
	if err != nil {
		handleCertificateError(c, err, "读取证书任务日志失败")
		return
	}
	core.HandleSuccess(c, result)
}

func CancelCertificateTask(c *gin.Context) {
	manager, ok := certificateManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Cancel(c.Param("id"))
	if err != nil {
		handleCertificateError(c, err, "取消证书任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func certificateManagerForRequest(c *gin.Context) (*certificateService.Manager, bool) {
	manager, err := getCertificateManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "证书任务服务不可用"))
		return nil, false
	}
	return manager, true
}

func handleCertificateError(c *gin.Context, err error, message string) {
	code := core.ErrBadRequest
	if errors.Is(err, gorm.ErrRecordNotFound) {
		code = core.ErrNotFound
	}
	core.HandleError(c, core.WrapError(err, code, message))
}

func positiveCertificateQueryInt(c *gin.Context, name string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(name, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func StopCertificateManager(ctx context.Context) error {
	certificateManagerMu.Lock()
	manager := certificateManager
	certificateManagerMu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Stop(ctx)
}
