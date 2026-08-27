package certificate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"oneinstack/app"
	"oneinstack/core"
	certificateService "oneinstack/internal/services/certificate"
	websiteService "oneinstack/internal/services/website"
	websiteHandler "oneinstack/router/handler/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ListAlgorithms(c *gin.Context) {
	core.HandleSuccess(c, certificateService.SupportedKeyAlgorithms())
}

func ListDNSProviders(c *gin.Context) {
	core.HandleSuccess(c, certificateService.SupportedDNSProviders())
}

func List(c *gin.Context) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	result, err := catalog.List(certificateService.CertificateListOptions{
		Page: positive(c.Query("page"), 1), PageSize: positive(c.Query("pageSize"), 20),
	})
	if err != nil {
		catalogError(c, err, "读取证书列表失败")
		return
	}
	core.HandleSuccess(c, result)
}

func Get(c *gin.Context) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	record, err := catalog.Get(c.Param("id"))
	if err != nil {
		catalogError(c, err, "读取证书详情失败")
		return
	}
	bindings, err := catalog.ListBindings(record.ID)
	if err != nil {
		catalogError(c, err, "读取证书绑定失败")
		return
	}
	core.HandleSuccess(c, gin.H{"certificate": record, "bindings": bindings})
}

func Upload(c *gin.Context) {
	var request input.CertificateUploadParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "上传证书参数格式不正确"))
		return
	}
	manager, err := websiteHandler.DefaultCertificateManager()
	if err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "证书任务服务不可用", certificateService.SafeCertificateErrorDetail(err)))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitManagedUpload(certificateService.CreateCertificateOptions{
		Domains: request.Domains, CertificatePEM: []byte(request.CertificatePEM), PrivateKeyPEM: []byte(request.PrivateKeyPEM),
		Remark: request.Remark, AutoRenew: request.AutoRenew, RenewBeforeDays: request.RenewBeforeDays,
	}, userID)
	if err != nil {
		catalogError(c, err, "创建上传证书任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func SelfSigned(c *gin.Context) {
	var request input.CertificateSelfSignedParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "自签证书参数格式不正确"))
		return
	}
	manager, err := websiteHandler.DefaultCertificateManager()
	if err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "证书任务服务不可用", certificateService.SafeCertificateErrorDetail(err)))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitManagedSelfSigned(certificateService.SelfSignedOptions{
		Domains: request.Domains, Algorithm: request.Algorithm, ValidityYears: request.ValidityYears,
		Remark: request.Remark, AutoRenew: request.AutoRenew, RenewBeforeDays: request.RenewBeforeDays,
	}, userID)
	if err != nil {
		catalogError(c, err, "创建自签证书任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func ReadCertificate(c *gin.Context) { readMaterial(c, false) }

func ReadPrivateKey(c *gin.Context) { readMaterial(c, true) }

func Download(c *gin.Context) {
	readMaterial(c, false)
}

func Bind(c *gin.Context) {
	var request input.CertificateBindingParam
	if err := c.ShouldBindJSON(&request); err != nil || request.WebsiteID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "网站绑定参数不完整"))
		return
	}
	manager, err := websiteHandler.DefaultCertificateManager()
	if err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "证书任务服务不可用", certificateService.SafeCertificateErrorDetail(err)))
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitManagedBind(c.Param("id"), request.WebsiteID, request.ForceHTTPS, userID)
	if err != nil {
		catalogError(c, err, "创建证书部署任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func Unbind(c *gin.Context) {
	websiteID, err := strconv.ParseInt(c.Param("websiteId"), 10, 64)
	if err != nil || websiteID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "websiteId 必须是正整数"))
		return
	}
	catalog, ok := certificateDeploymentCatalog(c)
	if !ok {
		return
	}
	if err := catalog.Unbind(context.Background(), c.Param("id"), websiteID); err != nil {
		catalogError(c, err, "解绑网站证书失败")
		return
	}
	core.HandleSuccess(c, gin.H{"status": "disabled"})
}

func Delete(c *gin.Context) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	if err := catalog.Delete(c.Param("id")); err != nil {
		catalogError(c, err, "删除证书失败")
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": true})
}

func ListDNSAccounts(c *gin.Context) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	accounts, err := catalog.ListDNSAccounts()
	if err != nil {
		catalogError(c, err, "读取 DNS 账号失败")
		return
	}
	core.HandleSuccess(c, accounts)
}

func SaveDNSAccount(c *gin.Context) {
	var request input.DNSAccountParam
	if err := c.ShouldBindJSON(&request); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "DNS 账号参数格式不正确"))
		return
	}
	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	account, err := catalog.SaveDNSAccount(request.ID, request.Name, request.Provider, request.CredentialOne, request.CredentialTwo, enabled)
	if err != nil {
		catalogError(c, err, "保存 DNS 账号失败")
		return
	}
	core.HandleSuccess(c, account)
}

func DeleteDNSAccount(c *gin.Context) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	if err := catalog.DeleteDNSAccount(c.Param("id")); err != nil {
		catalogError(c, err, "删除 DNS 账号失败")
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": true})
}

func ListTasks(c *gin.Context) {
	reader, ok := taskReader(c)
	if !ok {
		return
	}
	websiteID, _ := strconv.ParseInt(c.Query("websiteId"), 10, 64)
	result, err := reader.ListTasks(certificateService.TaskListOptions{WebsiteID: websiteID, Status: c.Query("status"), Page: positive(c.Query("page"), 1), PageSize: positive(c.Query("pageSize"), 20)})
	if err != nil {
		catalogError(c, err, "读取证书任务失败")
		return
	}
	core.HandleSuccess(c, result)
}

func GetTask(c *gin.Context) {
	reader, ok := taskReader(c)
	if !ok {
		return
	}
	task, err := reader.GetTask(c.Param("id"))
	if err != nil {
		catalogError(c, err, "读取证书任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func GetTaskLog(c *gin.Context) {
	reader, ok := taskReader(c)
	if !ok {
		return
	}
	result, err := reader.ReadTaskLog(c.Param("id"))
	if err != nil {
		catalogError(c, err, "读取证书任务日志失败")
		return
	}
	core.HandleSuccess(c, result)
}

func CancelTask(c *gin.Context) {
	manager, ok := taskManager(c)
	if !ok {
		return
	}
	task, err := manager.Cancel(c.Param("id"))
	if err != nil {
		catalogError(c, err, "取消证书任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func readMaterial(c *gin.Context, privateKey bool) {
	catalog, ok := certificateCatalog(c)
	if !ok {
		return
	}
	content, filename, err := catalog.ReadMaterial(c.Param("id"), privateKey)
	if err != nil {
		catalogError(c, err, "读取证书材料失败")
		return
	}
	c.Header("Content-Type", "application/x-pem-file")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Data(http.StatusOK, "application/x-pem-file", content)
}

func certificateCatalog(c *gin.Context) (*certificateService.Catalog, bool) {
	if app.DB() == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "证书数据库不可用"))
		return nil, false
	}
	return certificateService.NewCatalog(app.DB(), app.ONE_CONFIG.System.CertificatePath, nil), true
}

func certificateDeploymentCatalog(c *gin.Context) (*certificateService.Catalog, bool) {
	if app.DB() == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "证书数据库不可用"))
		return nil, false
	}
	deployer, err := websiteService.NewCertificateDeployer()
	if err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "证书部署服务不可用", certificateService.SafeCertificateErrorDetail(err)))
		return nil, false
	}
	return certificateService.NewCatalog(app.DB(), app.ONE_CONFIG.System.CertificatePath, deployer), true
}

func taskReader(c *gin.Context) (*certificateService.TaskReader, bool) {
	if app.DB() == nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "证书数据库不可用"))
		return nil, false
	}
	return certificateService.NewTaskReader(app.DB()), true
}

func taskManager(c *gin.Context) (*certificateService.Manager, bool) {
	manager, err := websiteHandler.DefaultCertificateManager()
	if err != nil {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "证书任务服务不可用", certificateService.SafeCertificateErrorDetail(err)))
		return nil, false
	}
	return manager, true
}

func catalogError(c *gin.Context, err error, message string) {
	var validationErr *certificateService.RequestValidationError
	if errors.As(err, &validationErr) {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, validationErr.Message, validationErr.Field))
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleError(c, core.NewError(core.ErrNotFound, message))
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "already has an active certificate task") {
		core.HandleError(c, core.NewErrorWithDetail(core.ErrConflict, "当前已有证书任务正在执行，请等待任务完成后重试", certificateService.SafeCertificateErrorDetail(err)))
		return
	}
	if strings.Contains(strings.ToLower(err.Error()), "required") || strings.Contains(strings.ToLower(err.Error()), "invalid") || strings.Contains(strings.ToLower(err.Error()), "unsupported") || strings.Contains(strings.ToLower(err.Error()), "cover") || strings.Contains(strings.ToLower(err.Error()), "match") {
		core.HandleError(c, core.NewError(core.ErrBadRequest, err.Error()))
		return
	}
	core.HandleError(c, core.NewErrorWithDetail(core.ErrInternalError, message, certificateService.SafeCertificateErrorDetail(err)))
}

func positive(value string, fallback int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
