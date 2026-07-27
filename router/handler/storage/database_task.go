package storage

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
	"oneinstack/internal/models"
	"oneinstack/internal/services/databasetask"
	storageService "oneinstack/internal/services/storage"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	databaseTaskManagerMu sync.Mutex
	databaseTaskManagerDB *gorm.DB
	databaseTaskManager   *databasetask.Manager
)

func getDatabaseTaskManager() (*databasetask.Manager, error) {
	databaseTaskManagerMu.Lock()
	defer databaseTaskManagerMu.Unlock()
	database := app.DB()
	if database == nil {
		return nil, errors.New("database is not initialized")
	}
	if databaseTaskManager == nil || databaseTaskManagerDB != database {
		backupRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_DATABASE_BACKUP_DIR"))
		if backupRoot == "" {
			backupRoot = filepath.Join(app.GetBasePath(), "backups", "database")
		}
		logRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_DATABASE_TASK_LOG_DIR"))
		if logRoot == "" {
			logRoot = filepath.Join(app.GetBasePath(), "logs", "database")
		}
		databaseTaskManager = databasetask.NewManager(
			database,
			backupRoot,
			logRoot,
			storageService.NewMySQLDatabaseOperator(),
		)
		databaseTaskManagerDB = database
	}
	if err := databaseTaskManager.Start(); err != nil {
		return nil, err
	}
	return databaseTaskManager, nil
}

// DefaultDatabaseTaskManager initializes recovery at process startup.
func DefaultDatabaseTaskManager() (*databasetask.Manager, error) {
	return getDatabaseTaskManager()
}

func CreateDatabaseBackup(c *gin.Context) {
	var req input.DatabaseBackupParam
	if err := c.ShouldBindJSON(&req); err != nil || req.LibraryID <= 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请选择要备份的数据库"))
		return
	}
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitBackup(req.LibraryID, userID)
	if err != nil {
		handleDatabaseTaskError(c, err, "创建数据库备份任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(task))
}

func RestoreDatabaseBackup(c *gin.Context) {
	var req input.DatabaseRestoreParam
	if err := c.ShouldBindJSON(&req); err != nil ||
		req.LibraryID <= 0 ||
		strings.TrimSpace(req.BackupID) == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "恢复参数不完整"))
		return
	}
	var library models.Library
	if err := app.DB().First(&library, req.LibraryID).Error; err != nil {
		handleDatabaseTaskError(c, err, "数据库不存在")
		return
	}
	if strings.TrimSpace(req.ConfirmName) != library.Name {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "数据库确认名称不匹配"))
		return
	}
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	task, err := manager.SubmitRestore(req.LibraryID, req.BackupID, userID)
	if err != nil {
		handleDatabaseTaskError(c, err, "创建数据库恢复任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponse(task))
}

func ListDatabaseTasks(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	page := positiveQueryInt(c, "page", 1)
	pageSize := positiveQueryInt(c, "pageSize", 20)
	libraryID, _ := strconv.ParseInt(c.Query("libraryId"), 10, 64)
	result, err := manager.ListTasks(databasetask.ListOptions{
		LibraryID: libraryID,
		Operation: c.Query("operation"),
		Status:    c.Query("status"),
		Page:      page,
		PageSize:  pageSize,
	})
	if err != nil {
		handleDatabaseTaskError(c, err, "读取数据库任务失败")
		return
	}
	core.HandleSuccess(c, result)
}

func GetDatabaseTask(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.GetTask(c.Param("id"))
	if err != nil {
		handleDatabaseTaskError(c, err, "读取数据库任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func CancelDatabaseTask(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Cancel(c.Param("id"))
	if err != nil {
		handleDatabaseTaskError(c, err, "取消数据库任务失败")
		return
	}
	core.HandleSuccess(c, task)
}

func GetDatabaseTaskLog(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	cursor, err := strconv.ParseInt(c.DefaultQuery("cursor", "0"), 10, 64)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "日志游标格式错误"))
		return
	}
	limit, err := strconv.ParseInt(c.DefaultQuery("limit", "65536"), 10, 64)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "日志读取大小格式错误"))
		return
	}
	chunk, err := manager.ReadLog(c.Param("id"), cursor, limit)
	if err != nil {
		handleDatabaseTaskError(c, err, "读取数据库任务日志失败")
		return
	}
	core.HandleSuccess(c, chunk)
}

func ListDatabaseBackups(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	libraryID, _ := strconv.ParseInt(c.Query("libraryId"), 10, 64)
	result, err := manager.ListBackups(
		libraryID,
		positiveQueryInt(c, "page", 1),
		positiveQueryInt(c, "pageSize", 20),
	)
	if err != nil {
		handleDatabaseTaskError(c, err, "读取数据库备份失败")
		return
	}
	core.HandleSuccess(c, result)
}

func DownloadDatabaseBackup(c *gin.Context) {
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	file, info, backup, err := manager.OpenBackup(c.Param("id"))
	if err != nil {
		handleDatabaseTaskError(c, err, "打开数据库备份失败")
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

func DeleteDatabaseBackup(c *gin.Context) {
	var req input.DeleteDatabaseBackupParam
	if err := c.ShouldBindJSON(&req); err != nil {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请输入数据库名称确认删除"))
		return
	}
	manager, ok := databaseManagerForRequest(c)
	if !ok {
		return
	}
	backup, err := manager.GetBackup(c.Param("id"))
	if err != nil {
		handleDatabaseTaskError(c, err, "数据库备份不存在")
		return
	}
	if strings.TrimSpace(req.ConfirmName) != backup.DatabaseName {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "数据库确认名称不匹配"))
		return
	}
	if err := manager.DeleteBackup(backup.ID); err != nil {
		handleDatabaseTaskError(c, err, "删除数据库备份失败")
		return
	}
	core.HandleSuccess(c, nil)
}

func databaseManagerForRequest(c *gin.Context) (*databasetask.Manager, bool) {
	manager, err := getDatabaseTaskManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "数据库任务服务不可用"))
		return nil, false
	}
	return manager, true
}

func handleDatabaseTaskError(c *gin.Context, err error, message string) {
	if databasetask.IsNotFound(err) {
		core.HandleError(c, core.WrapError(err, core.ErrNotFound, message))
		return
	}
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
}

func positiveQueryInt(c *gin.Context, key string, fallback int) int {
	value, err := strconv.Atoi(c.DefaultQuery(key, strconv.Itoa(fallback)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// Keep context imported in this package's public startup surface so callers
// can shut the manager down without reaching into implementation details.
func StopDatabaseTaskManager(ctx context.Context) error {
	databaseTaskManagerMu.Lock()
	manager := databaseTaskManager
	databaseTaskManagerMu.Unlock()
	if manager == nil {
		return nil
	}
	return manager.Stop(ctx)
}
