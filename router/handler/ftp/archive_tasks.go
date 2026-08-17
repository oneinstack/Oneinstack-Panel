package ftp

import (
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var archiveTaskQueue = make(chan string, 32)
var archiveTaskStarter sync.Once

// StartArchiveTaskManager starts the single-worker archive queue and resumes
// tasks that had been accepted before a panel restart.
func StartArchiveTaskManager() error {
	if app.DB() == nil {
		return fmt.Errorf("file archive task database is unavailable")
	}
	var startErr error
	archiveTaskStarter.Do(func() {
		var queued []models.FileArchiveTask
		if err := app.DB().Where("status = ?", models.FileArchiveTaskStatusQueued).Find(&queued).Error; err != nil {
			startErr = err
			return
		}
		go runArchiveTaskWorker()
		for _, task := range queued {
			archiveTaskQueue <- task.ID
		}
	})
	return startErr
}

func submitArchiveTask(input archiveTaskInput, requestedBy int64) (*models.FileArchiveTask, error) {
	if err := StartArchiveTaskManager(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	task := &models.FileArchiveTask{
		ID: uuid.NewString(), SourcePath: input.Path, TargetDir: input.TargetDir, ArchiveName: input.ArchiveName,
		Status: models.FileArchiveTaskStatusQueued, Message: "归档任务已进入队列", RequestedBy: requestedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := app.DB().Create(task).Error; err != nil {
		return nil, fmt.Errorf("create archive task: %w", err)
	}
	archiveTaskQueue <- task.ID
	return task, nil
}

func runArchiveTaskWorker() {
	for taskID := range archiveTaskQueue {
		runArchiveTask(taskID)
	}
}

func runArchiveTask(taskID string) {
	var task models.FileArchiveTask
	if err := app.DB().First(&task, "id = ? AND status = ?", taskID, models.FileArchiveTaskStatusQueued).Error; err != nil {
		return
	}
	now := time.Now().UTC()
	if err := app.DB().Model(&task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusRunning, "message": "正在创建压缩包", "started_at": now, "updated_at": now,
	}).Error; err != nil {
		return
	}
	manager, err := newArchiveTaskFileManager()
	var measured filemanager.OperationResult
	if err == nil {
		defer manager.Close()
		measured, err = manager.MeasureForArchive(task.SourcePath)
	}
	if err == nil {
		settings := currentFileSettings()
		reservation, reserveErr := reserveArchiveCapacity(manager, measured.Bytes, settings.capacityPolicy)
		if reserveErr != nil {
			err = reserveErr
		} else {
			defer reservation.Release()
			reporter := newArchiveTaskProgressReporter(&task)
			result, archiveErr := manager.ArchiveWithProgress(task.SourcePath, task.TargetDir, task.ArchiveName, reporter.Report)
			if archiveErr != nil {
				err = archiveErr
			} else {
				finishArchiveTask(&task, result)
				return
			}
		}
	}
	failArchiveTask(&task)
}

func reserveArchiveCapacity(manager *filemanager.Manager, bytes int64, policy filemanager.CapacityPolicy) (*filemanager.CapacityReservation, error) {
	reservation, _, err := manager.ReserveCapacity(bytes, policy)
	return reservation, err
}

func newArchiveTaskFileManager() (*filemanager.Manager, error) {
	rootPath := strings.TrimSpace(app.ONE_CONFIG.System.DefaultPath)
	manager, err := filemanager.New(rootPath)
	if err != nil {
		if fallbackRoot, ok := developmentFileRootFallback(rootPath); ok {
			manager, err = filemanager.New(fallbackRoot)
		}
	}
	if err != nil {
		return nil, err
	}
	return manager.WithProtectedPaths([]string{filepath.Join(app.GetBasePath(), ".ssh")}), nil
}

func finishArchiveTask(task *models.FileArchiveTask, result filemanager.OperationResult) {
	now := time.Now().UTC()
	_ = app.DB().Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusSucceeded, "message": "压缩包创建完成", "result_path": result.Path,
		"entries": result.Entries, "bytes": result.Bytes, "processed_bytes": result.Bytes, "progress": 100,
		"current_path": "", "finished_at": now, "updated_at": now,
	}).Error
}

type archiveTaskProgressReporter struct {
	task          *models.FileArchiveTask
	lastPersisted time.Time
	lastProgress  int
}

func newArchiveTaskProgressReporter(task *models.FileArchiveTask) *archiveTaskProgressReporter {
	return &archiveTaskProgressReporter{task: task, lastProgress: -1}
}

func (r *archiveTaskProgressReporter) Report(progress filemanager.ArchiveProgress) {
	percentage := 0
	if progress.TotalBytes > 0 {
		percentage = min(99, int(math.Floor(float64(progress.ProcessedBytes)*100/float64(progress.TotalBytes))))
	}
	now := time.Now().UTC()
	if percentage == r.lastProgress && now.Sub(r.lastPersisted) < 500*time.Millisecond {
		return
	}
	r.lastPersisted = now
	r.lastProgress = percentage
	_ = app.DB().Model(r.task).Updates(map[string]any{
		"total_bytes": progress.TotalBytes, "processed_bytes": progress.ProcessedBytes, "progress": percentage,
		"current_path": progress.CurrentPath, "updated_at": now,
	}).Error
}

func failArchiveTask(task *models.FileArchiveTask) {
	now := time.Now().UTC()
	_ = app.DB().Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusFailed, "message": "创建压缩包失败", "finished_at": now, "updated_at": now,
	}).Error
}

func GetArchiveTask(c *gin.Context) {
	var task models.FileArchiveTask
	if err := app.DB().First(&task, "id = ?", c.Param("id")).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrFileNotFound, "归档任务不存在"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok || task.RequestedBy != userID {
		core.HandleErrorWithStatus(c, http.StatusForbidden, core.NewError(core.ErrPermissionDenied, "无权查看该归档任务"))
		return
	}
	core.HandleSuccess(c, task)
}

func ListArchiveTasks(c *gin.Context) {
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	page, err := positiveQueryInt(c.Query("page"), 1, 1, 100000)
	if err != nil {
		handleBadRequest(c, err, "页码无效")
		return
	}
	pageSize, err := positiveQueryInt(c.Query("pageSize"), 20, 1, 100)
	if err != nil {
		handleBadRequest(c, err, "每页数量无效")
		return
	}
	status := strings.TrimSpace(c.Query("status"))
	if status != "" && status != models.FileArchiveTaskStatusQueued && status != models.FileArchiveTaskStatusRunning && status != models.FileArchiveTaskStatusSucceeded && status != models.FileArchiveTaskStatusFailed {
		handleBadRequest(c, fmt.Errorf("unsupported archive task status"), "归档任务状态无效")
		return
	}
	query := app.DB().Model(&models.FileArchiveTask{}).Where("requested_by = ?", userID)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取归档任务列表失败"))
		return
	}
	var tasks []models.FileArchiveTask
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取归档任务列表失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"data": tasks, "total": total, "page": page, "pageSize": pageSize})
}
