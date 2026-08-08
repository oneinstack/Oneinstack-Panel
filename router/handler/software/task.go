package software

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	accessservice "oneinstack/internal/services/access"
	softwareService "oneinstack/internal/services/software"
	"oneinstack/internal/services/softwaretask"
	storageService "oneinstack/internal/services/storage"
	websiteService "oneinstack/internal/services/website"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var (
	taskManagerMu sync.Mutex
	taskManagerDB *gorm.DB
	taskManager   *softwaretask.Manager
)

func getTaskManager() (*softwaretask.Manager, error) {
	taskManagerMu.Lock()
	defer taskManagerMu.Unlock()
	db := app.DB()
	if db == nil {
		return nil, errors.New("database is not initialized")
	}
	if taskManager == nil || taskManagerDB != db {
		logDir := strings.TrimSpace(os.Getenv("ONEINSTACK_INSTALL_LOG_DIR"))
		if logDir == "" {
			logDir = filepath.Join(app.GetBasePath(), "logs", "install")
		}
		taskManager = softwaretask.NewManager(
			db,
			logDir,
			func(
				ctx context.Context,
				request softwaretask.InstallRequest,
				logPath string,
				reporter *softwaretask.Reporter,
			) error {
				installer := softwareService.NewInstaller()
				if request.Operation == "uninstall" {
					_, err := installer.UninstallTask(
						ctx,
						&input.RemoveParams{
							Name:    request.Key,
							Version: request.Version,
						},
						logPath,
						reporter,
					)
					return err
				}
				if softwareService.IsServiceAction(request.Operation) {
					_, err := installer.ServiceActionTask(
						ctx,
						request.Key,
						request.Version,
						request.Operation,
						logPath,
						reporter,
					)
					return err
				}
				if request.Operation == "configure" {
					_, err := installer.ApplyServiceConfigurationTask(
						ctx,
						request.Key,
						request.Version,
						request.Revision,
						request.Configuration,
						logPath,
						reporter,
					)
					return err
				}
				params := &input.InstallParams{
					Key:      request.Key,
					Version:  request.Version,
					Port:     request.Port,
					Username: request.Username,
					Pwd:      request.Password,
				}
				if _, err := installer.InstallTask(ctx, params, logPath, reporter); err != nil {
					return err
				}
				if params.Key == "webserver" {
					if err := restoreManagedWebsiteConfigs(ctx); err != nil {
						return fmt.Errorf("restore managed website configurations: %w", err)
					}
				}
				if params.Key == "db" && params.Pwd != "" {
					if err := storageService.EnsureManagedLocalMySQLConnection(
						params.Port,
						params.Username,
						params.Pwd,
					); err != nil {
						return fmt.Errorf("register managed local MySQL connection: %w", err)
					}
				}
				if params.Key == "redis" {
					if err := storageService.EnsureManagedLocalRedisConnection(
						params.Port,
						"default",
						params.Pwd,
					); err != nil {
						return fmt.Errorf("register managed local Redis connection: %w", err)
					}
				}
				return nil
			},
		)
		taskManager.SetRecoveryInspector(func(
			ctx context.Context,
			task *models.SoftwareTask,
		) softwaretask.RecoveryInspection {
			status, message := softwareService.InspectTaskRecovery(
				ctx,
				task.Operation,
				task.SoftwareKey,
				task.Component,
				task.RequestedVersion,
			)
			return softwaretask.RecoveryInspection{Status: status, Message: message}
		})
		taskManagerDB = db
	}
	if err := taskManager.Start(); err != nil {
		return nil, err
	}
	return taskManager, nil
}

func restoreManagedWebsiteConfigs(ctx context.Context) error {
	var installed models.Software
	if err := app.DB().
		Where("`key` = ? AND installed = ?", "webserver", true).
		Order("id DESC").
		First(&installed).Error; err != nil {
		return fmt.Errorf("read installed Web server: %w", err)
	}
	component := strings.ToLower(strings.TrimSpace(installed.Component))
	if component == "" {
		component = "nginx"
	}
	if component != "nginx" && component != "openresty" {
		return nil
	}
	service, err := websiteService.DefaultService()
	if err != nil {
		return err
	}
	_, err = service.RestoreMissingManagedConfigs(ctx)
	return err
}

// DefaultTaskManager initializes the durable task service at server startup so
// interrupted task reconciliation and scheduled retention do not depend on the
// first API request.
func DefaultTaskManager() (*softwaretask.Manager, error) {
	return getTaskManager()
}

// SubmitInstallationTask exposes the same durable installation pipeline used
// by the software store to other trusted panel modules. Callers still provide
// a fixed, validated software key and version; no shell command is accepted.
func SubmitInstallationTask(
	req input.InstallParams,
	requestedBy int64,
) (*models.SoftwareTask, error) {
	if req.Key == "db" {
		req.Port = "3306"
		req.Username = "root"
		if strings.TrimSpace(req.Pwd) == "" {
			username, password, found, err := storageService.ManagedLocalMySQLCredential(req.Port)
			if err != nil {
				return nil, err
			}
			if found {
				req.Username = username
				req.Pwd = password
			} else {
				var installed int64
				if err := app.DB().Model(&models.Software{}).
					Where("`key` = ? AND installed = ?", "db", true).
					Count(&installed).Error; err != nil {
					return nil, fmt.Errorf("check current MySQL installation: %w", err)
				}
				if installed > 0 {
					return nil, fmt.Errorf("MySQL is installed but its managed root credential is unavailable")
				}
				password, err := utils.GenerateSecurePassword(24)
				if err != nil {
					return nil, err
				}
				req.Pwd = password
			}
		}
	}
	manager, err := getTaskManager()
	if err != nil {
		return nil, err
	}
	return manager.Submit(softwaretask.InstallRequest{
		Key:      req.Key,
		Version:  req.Version,
		Port:     req.Port,
		Username: req.Username,
		Password: req.Pwd,
	}, requestedBy)
}

func GetSoftwareTask(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Get(c.Param("id"))
	if err != nil {
		handleTaskLookupError(c, err)
		return
	}
	if !canAccessTask(c, task) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该软件任务"))
		return
	}
	core.HandleSuccess(c, task)
}

func ListSoftwareTasks(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	access, _ := middleware.UserAccess(c)
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("pageSize", "20"))
	result, err := manager.List(softwaretask.ListOptions{
		RequestedBy: userID,
		IncludeAll:  canReadAllSoftwareTasks(access),
		ActiveOnly:  parseQueryBool(c.Query("active")),
		Component:   c.Query("component"),
		Status:      c.Query("status"),
		Page:        page,
		PageSize:    pageSize,
	})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取软件任务失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func GetSoftwareTaskStats(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	access, _ := middleware.UserAccess(c)
	days, err := strconv.Atoi(c.DefaultQuery("days", "30"))
	if err != nil || days < 1 || days > 3650 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "统计天数必须在 1 到 3650 之间"))
		return
	}
	result, err := manager.Stats(softwaretask.TaskStatsOptions{
		RequestedBy: userID,
		IncludeAll:  canReadAllSoftwareTasks(access),
		Days:        days,
	})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取软件任务统计失败"))
		return
	}
	core.HandleSuccess(c, result)
}

func StreamSoftwareTaskEvents(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Get(c.Param("id"))
	if err != nil {
		handleTaskLookupError(c, err)
		return
	}
	if !canAccessTask(c, task) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该软件任务"))
		return
	}

	after := parseEventSequence(c.GetHeader("Last-Event-ID"))
	if queryAfter := parseEventSequence(c.Query("after")); queryAfter > after {
		after = queryAfter
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache, no-transform")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.Writer.Flush()

	notifications, unsubscribe := manager.Subscribe(task.ID)
	defer unsubscribe()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		events, err := manager.EventsAfter(task.ID, after, 200)
		if err != nil {
			return
		}
		for i := range events {
			event := &events[i]
			data, marshalErr := json.Marshal(event)
			if marshalErr != nil {
				continue
			}
			if _, err := fmt.Fprintf(
				c.Writer,
				"id: %d\nevent: %s\ndata: %s\n\n",
				event.Seq,
				event.Type,
				data,
			); err != nil {
				return
			}
			after = event.Seq
		}
		if len(events) > 0 {
			c.Writer.Flush()
		}
		task, err = manager.Get(task.ID)
		if err != nil {
			return
		}
		if models.IsSoftwareTaskTerminal(task.Status) && after >= task.EventSeq {
			return
		}

		select {
		case <-c.Request.Context().Done():
			return
		case <-notifications:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(c.Writer, ": heartbeat\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

func GetSoftwareTaskLog(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Get(c.Param("id"))
	if err != nil {
		handleTaskLookupError(c, err)
		return
	}
	if !canAccessTask(c, task) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权查看该软件任务"))
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
	chunk, err := manager.ReadLog(task.ID, cursor, limit)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "读取任务日志失败"))
		return
	}
	core.HandleSuccess(c, chunk)
}

func DownloadSoftwareTaskLog(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Get(c.Param("id"))
	if err != nil {
		handleTaskLookupError(c, err)
		return
	}
	if !canAccessTask(c, task) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权下载该任务日志"))
		return
	}
	file, info, downloadName, err := manager.OpenLog(task.ID)
	if errors.Is(err, os.ErrNotExist) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "任务日志不存在或已按保留策略清理"))
		return
	}
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "打开任务日志失败"))
		return
	}
	defer file.Close()
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})
	c.Header("Content-Disposition", disposition)
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, downloadName, info.ModTime(), file)
}

func CancelSoftwareTask(c *gin.Context) {
	manager, ok := taskManagerForRequest(c)
	if !ok {
		return
	}
	task, err := manager.Get(c.Param("id"))
	if err != nil {
		handleTaskLookupError(c, err)
		return
	}
	if !canAccessTask(c, task) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "无权取消该软件任务"))
		return
	}
	task, err = manager.Cancel(task.ID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "取消软件任务失败"))
		return
	}
	core.HandleSuccess(c, task)
}

func taskManagerForRequest(c *gin.Context) (*softwaretask.Manager, bool) {
	manager, err := getTaskManager()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "软件任务服务不可用"))
		return nil, false
	}
	return manager, true
}

func canAccessTask(c *gin.Context, task *models.SoftwareTask) bool {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		return false
	}
	access, _ := middleware.UserAccess(c)
	return canReadAllSoftwareTasks(access) || task.RequestedBy == userID
}

func canReadAllSoftwareTasks(access *accessservice.UserAccess) bool {
	return access != nil &&
		(access.IsSuperAdmin || access.HasPermission(accessservice.PermissionTaskReadAll))
}

func handleTaskLookupError(c *gin.Context, err error) {
	if softwaretask.IsNotFound(err) {
		core.HandleError(c, core.NewError(core.ErrNotFound, "软件任务不存在"))
		return
	}
	core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取软件任务失败"))
}

func parseEventSequence(value string) int64 {
	sequence, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || sequence < 0 {
		return 0
	}
	return sequence
}

func parseQueryBool(value string) bool {
	result, _ := strconv.ParseBool(value)
	return result
}
