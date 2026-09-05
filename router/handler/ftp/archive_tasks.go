package ftp

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
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
	"gorm.io/gorm"
)

var archiveTaskStarter sync.Once
var archiveTaskStartErr error

type archiveTaskQueueItem struct {
	id       string
	database *gorm.DB
}

var archiveTaskQueue = make(chan archiveTaskQueueItem, 32)

// StartArchiveTaskManager starts the single-worker archive queue and resumes
// tasks that had been accepted before a panel restart.
func StartArchiveTaskManager() error {
	database := app.DB()
	if database == nil {
		return fmt.Errorf("file archive task database is unavailable")
	}
	archiveTaskStarter.Do(func() {
		var interrupted []models.FileArchiveTask
		if err := database.Where("status = ?", models.FileArchiveTaskStatusRunning).Find(&interrupted).Error; err != nil {
			archiveTaskStartErr = err
			return
		}
		now := time.Now().UTC()
		for index := range interrupted {
			message := "面板重启导致归档任务中断，请重新提交"
			code := "FILE_ARCHIVE_INTERRUPTED"
			if interrupted[index].Operation == models.FileArchiveTaskOperationExtract {
				message = "面板重启导致解压任务中断，请重新提交"
				code = "FILE_EXTRACT_INTERRUPTED"
			}
			if err := database.Model(&interrupted[index]).Updates(map[string]any{
				"status": models.FileArchiveTaskStatusFailed, "message": message, "error_code": code,
				"finished_at": now, "updated_at": now,
			}).Error; err != nil {
				archiveTaskStartErr = err
				return
			}
		}
		var queued []models.FileArchiveTask
		if err := database.Where("status = ?", models.FileArchiveTaskStatusQueued).Find(&queued).Error; err != nil {
			archiveTaskStartErr = err
			return
		}
		go runArchiveTaskWorker()
		for _, task := range queued {
			archiveTaskQueue <- archiveTaskQueueItem{id: task.ID, database: database}
		}
	})
	return archiveTaskStartErr
}

func submitArchiveTask(input archiveTaskInput, requestedBy int64, rootPath string, capacityPolicy filemanager.CapacityPolicy) (*models.FileArchiveTask, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("%w: file root is empty", filemanager.ErrInvalidPath)
	}
	if err := StartArchiveTaskManager(); err != nil {
		return nil, err
	}
	database := app.DB()
	if database == nil {
		return nil, fmt.Errorf("file archive task database is unavailable")
	}
	now := time.Now().UTC()
	task := &models.FileArchiveTask{
		ID: uuid.NewString(), Operation: models.FileArchiveTaskOperationArchive,
		SourcePath: input.Path, TargetDir: input.TargetDir, ArchiveName: input.ArchiveName,
		FileRootPath: rootPath, QuotaBytes: capacityPolicy.QuotaBytes, MinFreeBytes: capacityPolicy.MinFreeBytes,
		Status: models.FileArchiveTaskStatusQueued, Message: "归档任务已进入队列", RequestedBy: requestedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create archive task: %w", err)
	}
	archiveTaskQueue <- archiveTaskQueueItem{id: task.ID, database: database}
	return task, nil
}

func runArchiveTaskWorker() {
	for item := range archiveTaskQueue {
		runArchiveTask(item.database, item.id)
	}
}

func runArchiveTask(database *gorm.DB, taskID string) {
	var task models.FileArchiveTask
	if err := database.First(&task, "id = ? AND status = ?", taskID, models.FileArchiveTaskStatusQueued).Error; err != nil {
		return
	}
	message := "正在创建压缩包"
	if task.Operation == models.FileArchiveTaskOperationExtract {
		message = "正在解压文件"
	}
	now := time.Now().UTC()
	if err := database.Model(&task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusRunning, "message": message, "started_at": now, "updated_at": now,
	}).Error; err != nil {
		return
	}
	manager, err := newArchiveTaskFileManager(task.FileRootPath)
	if err != nil {
		failArchiveTask(database, &task, err)
		return
	}
	defer manager.Close()
	if task.Operation == models.FileArchiveTaskOperationExtract {
		runExtractTask(database, manager, &task)
		return
	}
	runCreateArchiveTask(database, manager, &task)
}

func runCreateArchiveTask(database *gorm.DB, manager *filemanager.Manager, task *models.FileArchiveTask) {
	measured, err := manager.MeasureForArchive(task.SourcePath)
	if err == nil {
		reservation, reserveErr := reserveArchiveCapacity(manager, measured.Bytes, filemanager.CapacityPolicy{
			QuotaBytes: task.QuotaBytes, MinFreeBytes: task.MinFreeBytes,
		})
		if reserveErr != nil {
			err = reserveErr
		} else {
			defer reservation.Release()
			reporter := newArchiveTaskProgressReporter(database, task)
			result, archiveErr := archiveWithAvailableName(database, manager, task, reporter.Report)
			if archiveErr != nil {
				err = archiveErr
			} else {
				finishArchiveTask(database, task, result)
				return
			}
		}
	}
	failArchiveTask(database, task, err)
}

func archiveWithAvailableName(database *gorm.DB, manager *filemanager.Manager, task *models.FileArchiveTask, report filemanager.ArchiveProgressFunc) (filemanager.OperationResult, error) {
	requestedName := task.ArchiveName
	for attempt := 0; attempt < 10; attempt++ {
		name, err := nextArchiveName(manager, task.TargetDir, requestedName)
		if err != nil {
			return filemanager.OperationResult{}, err
		}
		if name != task.ArchiveName {
			task.ArchiveName = name
			if err := database.Model(task).Updates(map[string]any{"archive_name": name, "updated_at": time.Now().UTC()}).Error; err != nil {
				return filemanager.OperationResult{}, err
			}
		}
		result, err := manager.ArchiveWithProgress(task.SourcePath, task.TargetDir, name, report)
		if !errors.Is(err, fs.ErrExist) {
			return result, err
		}
	}
	return filemanager.OperationResult{}, fmt.Errorf("%w: failed to allocate unique archive name", fs.ErrExist)
}

func nextArchiveName(manager *filemanager.Manager, targetDir, requestedName string) (string, error) {
	requestedName = strings.TrimSpace(requestedName)
	if !strings.HasSuffix(strings.ToLower(requestedName), ".tar.gz") {
		return "", filemanager.ErrInvalidName
	}
	exists, err := archiveNameExists(manager, targetDir, requestedName)
	if err != nil {
		return "", err
	}
	if !exists {
		return requestedName, nil
	}
	base := requestedName[:len(requestedName)-len(".tar.gz")]
	for sequence := 1; sequence <= 1000; sequence++ {
		candidate := fmt.Sprintf("%s(%d).tar.gz", base, sequence)
		exists, err := archiveNameExists(manager, targetDir, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: too many archive files with the same name", fs.ErrExist)
}

func archiveNameExists(manager *filemanager.Manager, targetDir, name string) (bool, error) {
	relative, err := manager.Join(targetDir, name)
	if err != nil {
		return false, err
	}
	_, _, err = manager.Stat(manager.VirtualPath(relative))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func reserveArchiveCapacity(manager *filemanager.Manager, bytes int64, policy filemanager.CapacityPolicy) (*filemanager.CapacityReservation, error) {
	reservation, _, err := manager.ReserveCapacity(bytes, policy)
	return reservation, err
}

func newArchiveTaskFileManager(rootPath string) (*filemanager.Manager, error) {
	rootPath = strings.TrimSpace(rootPath)
	manager, err := filemanager.New(rootPath)
	if err != nil {
		return nil, err
	}
	return manager.WithProtectedPaths([]string{filepath.Join(app.GetBasePath(), ".ssh")}), nil
}

func finishArchiveTask(database *gorm.DB, task *models.FileArchiveTask, result filemanager.OperationResult) {
	now := time.Now().UTC()
	_ = database.Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusSucceeded, "message": "压缩包创建完成", "result_path": result.Path,
		"entries": result.Entries, "bytes": result.Bytes, "processed_bytes": result.Bytes, "progress": 100,
		"current_path": "", "finished_at": now, "updated_at": now,
	}).Error
}

type archiveTaskProgressReporter struct {
	database      *gorm.DB
	task          *models.FileArchiveTask
	lastPersisted time.Time
	lastProgress  int
}

func newArchiveTaskProgressReporter(database *gorm.DB, task *models.FileArchiveTask) *archiveTaskProgressReporter {
	return &archiveTaskProgressReporter{database: database, task: task, lastProgress: -1}
}

func (r *archiveTaskProgressReporter) Report(progress filemanager.ArchiveProgress) {
	r.persist(progress.ProcessedBytes, progress.TotalBytes, progress.Entries, progress.CurrentPath)
}

func (r *archiveTaskProgressReporter) ReportExtract(progress filemanager.ExtractProgress) {
	r.persist(progress.ProcessedBytes, progress.TotalBytes, progress.Entries, progress.CurrentPath)
}

func (r *archiveTaskProgressReporter) persist(processedBytes, totalBytes int64, entries int, currentPath string) {
	percentage := 0
	if totalBytes > 0 {
		percentage = min(99, int(math.Floor(float64(processedBytes)*100/float64(totalBytes))))
	}
	now := time.Now().UTC()
	if percentage == r.lastProgress && now.Sub(r.lastPersisted) < 500*time.Millisecond {
		return
	}
	r.lastPersisted = now
	r.lastProgress = percentage
	_ = r.database.Model(r.task).Updates(map[string]any{
		"total_bytes": totalBytes, "processed_bytes": processedBytes, "progress": percentage,
		"current_path": currentPath, "entries": entries, "updated_at": now,
	}).Error
}

func failArchiveTask(database *gorm.DB, task *models.FileArchiveTask, cause error) {
	now := time.Now().UTC()
	code, message := archiveTaskFailure(cause)
	log.Printf("file archive task failed task_id=%s code=%s cause=%v", task.ID, code, cause)
	_ = database.Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusFailed, "message": message, "error_code": code,
		"finished_at": now, "updated_at": now,
	}).Error
}

func archiveTaskFailure(cause error) (code, message string) {
	switch {
	case errors.Is(cause, fs.ErrExist):
		return "FILE_ARCHIVE_EXISTS", "压缩包已存在，请更换名称"
	case errors.Is(cause, fs.ErrPermission):
		return "FILE_ARCHIVE_PERMISSION_DENIED", "无权在目标目录创建压缩包"
	case errors.Is(cause, fs.ErrNotExist):
		return "FILE_ARCHIVE_PATH_NOT_FOUND", "压缩源或目标目录不存在"
	case errors.Is(cause, filemanager.ErrQuotaExceeded), errors.Is(cause, filemanager.ErrInsufficientSpace):
		return "FILE_ARCHIVE_INSUFFICIENT_SPACE", "创建压缩包所需存储容量不足"
	case errors.Is(cause, filemanager.ErrInvalidName):
		return "FILE_ARCHIVE_INVALID_NAME", "压缩包名称无效"
	case errors.Is(cause, filemanager.ErrInvalidPath), errors.Is(cause, filemanager.ErrRootOperation):
		return "FILE_ARCHIVE_INVALID_PATH", "归档路径无效或已发生变化"
	default:
		return "FILE_ARCHIVE_FAILED", "创建压缩包失败"
	}
}

func GetArchiveTask(c *gin.Context) {
	var task models.FileArchiveTask
	if err := app.DB().First(&task, "id = ? AND operation = ?", c.Param("id"), models.FileArchiveTaskOperationArchive).Error; err != nil {
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
	query := app.DB().Model(&models.FileArchiveTask{}).Where("requested_by = ? AND operation = ?", userID, models.FileArchiveTaskOperationArchive)
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
