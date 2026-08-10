package website

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/services/storage"
	websiteService "oneinstack/internal/services/website"
	"oneinstack/internal/services/websitetask"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	websiteTaskManagerMu sync.Mutex
	websiteTaskManagerDB *gorm.DB
	websiteTaskManager   *websitetask.Manager
)

func getWebsiteTaskManager() (*websitetask.Manager, error) {
	websiteTaskManagerMu.Lock()
	defer websiteTaskManagerMu.Unlock()
	database := app.DB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	if websiteTaskManager == nil || websiteTaskManagerDB != database {
		service, err := websiteService.DefaultService()
		if err != nil {
			return nil, err
		}
		backupRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_WEBSITE_BACKUP_DIR"))
		if backupRoot == "" {
			backupRoot = filepath.Join(app.GetBasePath(), "backups", "website")
		}
		logRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_WEBSITE_TASK_LOG_DIR"))
		if logRoot == "" {
			logRoot = filepath.Join(app.GetBasePath(), "logs", "website")
		}
		websiteTaskManager = websitetask.NewManager(
			database,
			backupRoot,
			logRoot,
			service,
			storage.NewMySQLDatabaseOperator(),
			app.ONE_CONFIG.System.WebsiteBackupMaxBytes,
			app.ONE_CONFIG.System.WebsiteBackupMaxFiles,
			app.ONE_CONFIG.System.FileMinFreeBytes,
		)
		websiteTaskManagerDB = database
	}
	if err := websiteTaskManager.Start(); err != nil {
		return nil, err
	}
	return websiteTaskManager, nil
}

// DefaultWebsiteTaskManager initializes restart recovery during Panel startup.
func DefaultWebsiteTaskManager() (*websitetask.Manager, error) {
	return getWebsiteTaskManager()
}

func StopWebsiteTaskManager(ctx context.Context) error {
	websiteTaskManagerMu.Lock()
	manager := websiteTaskManager
	websiteTaskManagerMu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Stop(ctx)
}

func CreateWebsiteBackup(c *gin.Context) {
	var request input.WebsiteBackupParam
	if err := c.ShouldBindJSON(&request); err != nil || request.WebsiteID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请选择要备份的网站"))
		return
	}
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitBackup(request.WebsiteID, request.DatabaseID, userID)
	if err != nil {
		handleWebsiteTaskError(c, err, "创建网站备份任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func RestoreWebsiteBackup(c *gin.Context) {
	var request input.WebsiteRestoreParam
	if err := c.ShouldBindJSON(&request); err != nil ||
		strings.TrimSpace(request.BackupID) == "" ||
		strings.TrimSpace(request.ConfirmName) == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "网站恢复参数不完整"))
		return
	}
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	if shouldRequestWebsiteApproval(c) {
		approval, err := createWebsiteApproval(c, ApprovalActionWebsiteRestore, request.ConfirmName, request.BackupID, RestoreApprovalPayload{
			BackupID:    request.BackupID,
			ConfirmName: request.ConfirmName,
		})
		if err != nil {
			core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "创建网站恢复审批失败"))
			return
		}
		c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
			"mode":       "approval_pending",
			"approvalId": approval.ID,
			"status":     approval.Status,
		}))
		return
	}
	task, err := manager.SubmitRestore(request.BackupID, request.ConfirmName, userID)
	if err != nil {
		handleWebsiteTaskError(c, err, "创建网站恢复任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, task))
}

func ListWebsiteTasks(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	websiteID, _ := strconv.ParseInt(c.Query("websiteId"), 10, 64)
	result, err := manager.ListTasks(websitetask.ListOptions{
		WebsiteID:   websiteID,
		Operation:   c.Query("operation"),
		Status:      c.Query("status"),
		RequestedBy: userID,
		IncludeAll:  canReadAllWebsiteTasks(access),
		Page:        positiveWebsiteQueryInt(c, "page", 1),
		PageSize:    positiveWebsiteQueryInt(c, "pageSize", 20),
	})
	if err != nil {
		handleWebsiteTaskError(c, err, "读取网站任务失败")
		return
	}
	core.HandleSuccess(c, result)
}

func GetWebsiteTask(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.GetTask(c.Param("id"))
	if err != nil {
		handleWebsiteTaskError(c, err, "读取网站任务失败")
		return
	}
	if !canAccessWebsiteTask(c, task.RequestedBy) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该网站任务"))
		return
	}
	core.HandleSuccess(c, task)
}

func CancelWebsiteTask(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.GetTask(c.Param("id"))
	if err != nil {
		handleWebsiteTaskError(c, err, "读取网站任务失败")
		return
	}
	if !canAccessWebsiteTask(c, task.RequestedBy) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权取消该网站任务"))
		return
	}
	task, err = manager.Cancel(c.Param("id"))
	if err != nil {
		handleWebsiteTaskError(c, err, "取消网站任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func GetWebsiteTaskLog(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	task, taskErr := manager.GetTask(c.Param("id"))
	if taskErr != nil {
		handleWebsiteTaskError(c, taskErr, "读取网站任务失败")
		return
	}
	if !canAccessWebsiteTask(c, task.RequestedBy) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该网站任务日志"))
		return
	}
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil || cursor < 0 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "cursor 必须是大于等于 0 的整数", "cursor"))
		return
	}
	limit, err := strconv.ParseInt(c.DefaultQuery("limit", "65536"), 10, 64)
	if err != nil || limit < 1 || limit > 65536 {
		core.HandleError(c, core.NewFieldError(core.ErrInvalidParameter, "limit 必须是 1 到 65536 之间的整数", "limit"))
		return
	}
	chunk, err := manager.ReadLog(c.Param("id"), cursor, limit)
	if err != nil {
		handleWebsiteTaskError(c, err, "读取网站任务日志失败")
		return
	}
	core.HandleSuccess(c, chunk)
}

func ListWebsiteBackups(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	websiteID, _ := strconv.ParseInt(c.Query("websiteId"), 10, 64)
	userID, _ := middleware.AuthenticatedUserID(c)
	access, _ := middleware.UserAccess(c)
	result, err := manager.ListBackups(
		websiteID,
		userID,
		canReadAllWebsiteTasks(access),
		positiveWebsiteQueryInt(c, "page", 1),
		positiveWebsiteQueryInt(c, "pageSize", 20),
	)
	if err != nil {
		handleWebsiteTaskError(c, err, "读取网站备份失败")
		return
	}
	core.HandleSuccess(c, result)
}

func DownloadWebsiteBackup(c *gin.Context) {
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	file, info, backup, err := manager.OpenBackup(c.Param("id"))
	if err != nil {
		handleWebsiteTaskError(c, err, "打开网站备份失败")
		return
	}
	defer file.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": backup.FileName})
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Type", "application/gzip")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Checksum-SHA256", backup.SHA256)
	http.ServeContent(c.Writer, c.Request, backup.FileName, info.ModTime(), file)
}

func DeleteWebsiteBackup(c *gin.Context) {
	var request input.DeleteWebsiteBackupParam
	if err := c.ShouldBindJSON(&request); err != nil ||
		strings.TrimSpace(request.ConfirmName) == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入网站名称确认删除"))
		return
	}
	manager, ok := websiteManagerForRequest(c)
	if !ok {
		return
	}
	backup, err := manager.GetBackup(c.Param("id"))
	if err != nil {
		handleWebsiteTaskError(c, err, "网站备份不存在")
		return
	}
	if strings.TrimSpace(request.ConfirmName) != backup.WebsiteName {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "网站确认名称不匹配"))
		return
	}
	if err := manager.DeleteBackup(backup.ID); err != nil {
		handleWebsiteTaskError(c, err, "删除网站备份失败")
		return
	}
	core.HandleSuccess(c, nil)
}

func websiteManagerForRequest(c *gin.Context) (*websitetask.Manager, bool) {
	manager, err := getWebsiteTaskManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "网站任务服务不可用"))
		return nil, false
	}
	return manager, true
}

func handleWebsiteTaskError(c *gin.Context, err error, message string) {
	if websitetask.IsNotFound(err) {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, message))
		return
	}
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
}

func positiveWebsiteQueryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(key, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
