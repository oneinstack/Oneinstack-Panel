package ftp

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func submitExtractTask(input extractTaskInput, requestedBy int64, rootPath, archiveFormat string, settings fileSettings) (*models.FileArchiveTask, error) {
	if strings.TrimSpace(rootPath) == "" {
		return nil, fmt.Errorf("%w: file root is empty", filemanager.ErrInvalidPath)
	}
	if err := StartArchiveTaskManager(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	task := &models.FileArchiveTask{
		ID: uuid.NewString(), Operation: models.FileArchiveTaskOperationExtract,
		SourcePath: input.Path, TargetDir: input.TargetDir, ArchiveFormat: archiveFormat, Overwrite: input.Overwrite,
		FileRootPath: rootPath, QuotaBytes: settings.capacityPolicy.QuotaBytes, MinFreeBytes: settings.capacityPolicy.MinFreeBytes,
		MaxExtractBytes: settings.extractMaxBytes, MaxExtractFiles: settings.extractMaxFiles,
		Status: models.FileArchiveTaskStatusQueued, Message: "解压任务已进入队列", RequestedBy: requestedBy,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := app.DB().Create(task).Error; err != nil {
		return nil, fmt.Errorf("create extract task: %w", err)
	}
	archiveTaskQueue <- task.ID
	return task, nil
}

func runExtractTask(manager *filemanager.Manager, task *models.FileArchiveTask) {
	reporter := newArchiveTaskProgressReporter(task)
	result, err := manager.ExtractWithProgress(task.SourcePath, task.TargetDir, filemanager.ExtractOptions{
		Overwrite: task.Overwrite, MaxBytes: task.MaxExtractBytes, MaxEntries: task.MaxExtractFiles,
		CapacityPolicy: filemanager.CapacityPolicy{QuotaBytes: task.QuotaBytes, MinFreeBytes: task.MinFreeBytes},
	}, reporter.ReportExtract)
	if err != nil {
		failExtractTask(task, err)
		return
	}
	finishExtractTask(task, result)
}

func finishExtractTask(task *models.FileArchiveTask, result filemanager.OperationResult) {
	now := time.Now().UTC()
	_ = app.DB().Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusSucceeded, "message": "文件解压完成", "result_path": result.Path,
		"entries": result.Entries, "bytes": result.Bytes, "total_bytes": result.Bytes,
		"processed_bytes": result.Bytes, "progress": 100, "current_path": "", "finished_at": now, "updated_at": now,
	}).Error
}

func failExtractTask(task *models.FileArchiveTask, cause error) {
	now := time.Now().UTC()
	code, message := extractTaskFailure(cause)
	if code == "FILE_EXTRACT_TARGET_CONFLICT" {
		target := strings.TrimSpace(task.TargetDir)
		if target == "" {
			target = "指定目录"
		}
		message = fmt.Sprintf("解压目标目录 %s 存在同名文件，未覆盖原文件；请选择其他目录或确认覆盖后重试", target)
	}
	log.Printf("file extract task failed task_id=%s code=%s cause=%v", task.ID, code, cause)
	_ = app.DB().Model(task).Updates(map[string]any{
		"status": models.FileArchiveTaskStatusFailed, "message": message, "error_code": code,
		"finished_at": now, "updated_at": now,
	}).Error
}

func extractTaskFailure(cause error) (code, message string) {
	switch {
	case errors.Is(cause, filemanager.ErrArchiveUnsupportedFormat):
		return "FILE_EXTRACT_UNSUPPORTED_FORMAT", "暂不支持该压缩格式"
	case errors.Is(cause, filemanager.ErrArchiveFormatMismatch):
		return "FILE_EXTRACT_FORMAT_MISMATCH", "压缩文件内容与扩展名不匹配"
	case errors.Is(cause, filemanager.ErrArchiveEncrypted):
		return "FILE_EXTRACT_ENCRYPTED", "暂不支持加密压缩文件"
	case errors.Is(cause, filemanager.ErrArchiveMultiVolume):
		return "FILE_EXTRACT_MULTI_VOLUME", "暂不支持分卷压缩文件"
	case errors.Is(cause, filemanager.ErrArchiveUnsafePath):
		return "FILE_EXTRACT_UNSAFE_PATH", "压缩包包含不安全路径，已拒绝解压"
	case errors.Is(cause, filemanager.ErrArchiveUnsupportedEntry):
		return "FILE_EXTRACT_UNSUPPORTED_ENTRY", "压缩包包含链接或特殊文件，已拒绝解压"
	case errors.Is(cause, filemanager.ErrArchiveLimitExceeded):
		return "FILE_EXTRACT_LIMIT_EXCEEDED", "压缩包展开后超过文件数量或容量限制"
	case errors.Is(cause, filemanager.ErrArchiveTargetConflict), errors.Is(cause, fs.ErrExist),
		errors.Is(cause, syscall.EISDIR), errors.Is(cause, syscall.ENOTEMPTY):
		return "FILE_EXTRACT_TARGET_CONFLICT", "目标目录存在同名文件，请确认覆盖后重试"
	case errors.Is(cause, filemanager.ErrArchiveRollbackFailed):
		return "FILE_EXTRACT_ROLLBACK_FAILED", "解压提交失败且未能完整回滚，请检查目标目录"
	case errors.Is(cause, fs.ErrPermission), errors.Is(cause, syscall.EPERM), errors.Is(cause, syscall.EROFS):
		return "FILE_EXTRACT_PERMISSION_DENIED", "无权读取压缩文件或写入目标目录"
	case errors.Is(cause, fs.ErrNotExist):
		return "FILE_EXTRACT_PATH_NOT_FOUND", "压缩文件或目标父目录不存在"
	case errors.Is(cause, filemanager.ErrQuotaExceeded), errors.Is(cause, filemanager.ErrInsufficientSpace),
		errors.Is(cause, syscall.ENOSPC), errors.Is(cause, syscall.EDQUOT):
		return "FILE_EXTRACT_INSUFFICIENT_SPACE", "解压所需存储容量不足"
	case errors.Is(cause, syscall.EXDEV):
		return "FILE_EXTRACT_FILESYSTEM_BOUNDARY", "解压提交失败：源文件与目标目录不在同一文件系统"
	case errors.Is(cause, syscall.ENOSYS), errors.Is(cause, syscall.EOPNOTSUPP):
		return "FILE_EXTRACT_FILESYSTEM_UNSUPPORTED", "解压提交失败：当前文件系统不支持安全提交操作"
	case errors.Is(cause, syscall.EIO):
		return "FILE_EXTRACT_FILESYSTEM_IO", "解压失败：文件系统读写异常，请检查磁盘或存储状态"
	case errors.Is(cause, syscall.EINVAL):
		return "FILE_EXTRACT_INVALID_OPERATION", "解压提交失败：文件系统拒绝当前操作，请检查目标目录和文件系统状态"
	case errors.Is(cause, filemanager.ErrArchiveInvalid):
		return "FILE_EXTRACT_INVALID_ARCHIVE", "压缩文件损坏或内容无效"
	case errors.Is(cause, filemanager.ErrInvalidPath), errors.Is(cause, filemanager.ErrReservedPath),
		errors.Is(cause, filemanager.ErrRootOperation), errors.Is(cause, filemanager.ErrNotRegular), errors.Is(cause, syscall.ENOTDIR):
		return "FILE_EXTRACT_INVALID_PATH", "解压源或目标路径无效"
	default:
		return "FILE_EXTRACT_FAILED", "文件解压失败：执行阶段未能完成，请检查源文件完整性、目标目录权限、剩余空间和文件系统状态后重试"
	}
}

func GetExtractTask(c *gin.Context) {
	var task models.FileArchiveTask
	if err := app.DB().First(&task, "id = ? AND operation = ?", c.Param("id"), models.FileArchiveTaskOperationExtract).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrFileNotFound, "解压任务不存在"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok || task.RequestedBy != userID {
		core.HandleErrorWithStatus(c, http.StatusForbidden, core.NewError(core.ErrPermissionDenied, "无权查看该解压任务"))
		return
	}
	core.HandleSuccess(c, task)
}

func ListExtractTasks(c *gin.Context) {
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
		handleBadRequest(c, fmt.Errorf("unsupported extract task status"), "解压任务状态无效")
		return
	}
	query := app.DB().Model(&models.FileArchiveTask{}).Where("requested_by = ? AND operation = ?", userID, models.FileArchiveTaskOperationExtract)
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取解压任务列表失败"))
		return
	}
	var tasks []models.FileArchiveTask
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&tasks).Error; err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "读取解压任务列表失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"data": tasks, "total": total, "page": page, "pageSize": pageSize})
}
