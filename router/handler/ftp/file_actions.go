package ftp

import (
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	pathpkg "path"
	"strings"
	"syscall"
	"time"

	"oneinstack/core"
	accessservice "oneinstack/internal/services/access"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

type transferInput struct {
	SourcePath string `json:"sourcePath" binding:"required"`
	TargetPath string `json:"targetPath" binding:"required"`
	Overwrite  bool   `json:"overwrite"`
}

func CopyFileOrDir(c *gin.Context) {
	var input transferInput
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "复制文件或目录参数格式不正确")
		return
	}
	targetDir, targetName := splitTargetPath(input.TargetPath)
	startFileOperation(c, "file.copy", operationAuditPath(input.SourcePath, targetDir, targetName))
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	measured, err := manager.Measure(input.SourcePath)
	if err != nil {
		handleFileError(c, err, copyFailureMessage("source", input, err))
		return
	}
	settings := currentFileSettings()
	reservation, _, err := manager.ReserveCapacity(measured.Bytes, settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "复制所需存储容量不足")
		return
	}
	defer reservation.Release()
	result, err := manager.Copy(input.SourcePath, targetDir, targetName, input.Overwrite)
	if err != nil {
		handleFileError(c, err, copyFailureMessage("target", input, err))
		return
	}
	core.HandleSuccess(c, result)
	finishFileOperation(c, "success", fmt.Sprintf("复制 %d 项，共 %d 字节", result.Entries, result.Bytes))
}

func copyFailureMessage(defaultPhase string, input transferInput, err error) string {
	phase := defaultPhase
	failedPath := strings.TrimSpace(input.SourcePath)
	cause := err
	var pathErr *filemanager.CopyPathError
	if errors.As(err, &pathErr) {
		if pathErr.Phase != "" {
			phase = pathErr.Phase
		}
		if pathErr.Path != "" {
			failedPath = pathErr.Path
		}
		if pathErr.Cause != nil {
			cause = pathErr.Cause
		}
	}

	source := strings.TrimSpace(input.SourcePath)
	target := strings.TrimSpace(input.TargetPath)
	context := fmt.Sprintf("源：%s；目标：%s；失败路径：%s", source, target, failedPath)
	if errors.Is(cause, fs.ErrExist) {
		return fmt.Sprintf("复制目标已存在且未开启覆盖，请开启覆盖或更换目标名称（%s）", context)
	}
	if errors.Is(cause, fs.ErrNotExist) {
		if phase == "target" {
			return fmt.Sprintf("写入复制目标失败：目标目录或目标路径在复制期间不存在，可能已被其他操作移除（%s）", context)
		}
		return fmt.Sprintf("读取复制源失败：源路径不存在，或源内容在复制期间发生变化（%s）", context)
	}
	if errors.Is(cause, fs.ErrPermission) {
		if phase == "target" {
			return fmt.Sprintf("写入复制目标失败：目标目录或文件没有写入权限（%s）", context)
		}
		return fmt.Sprintf("读取复制源失败：源目录或文件没有读取权限（%s）", context)
	}
	if errors.Is(cause, filemanager.ErrReservedPath) {
		return fmt.Sprintf("复制被拒绝：路径属于面板内部目录或受保护目录，不能读取、写入或通过符号链接间接访问（%s）", context)
	}
	if errors.Is(cause, filemanager.ErrUnsupportedType) {
		return fmt.Sprintf("读取复制源失败：源目录包含不支持的文件类型，或包含指向复制范围外的符号链接；当前安全边界仅允许普通文件、目录，以及目标仍在源目录内部的符号链接（%s）", context)
	}
	if errors.Is(cause, filemanager.ErrInvalidPath) {
		return fmt.Sprintf("复制路径无效：目标不能是源本身或源目录的子路径，且符号链接不能逃出授权根目录（%s）", context)
	}
	if errors.Is(cause, syscall.ENOSPC) || errors.Is(cause, syscall.EDQUOT) || errors.Is(cause, filemanager.ErrInsufficientSpace) || errors.Is(cause, filemanager.ErrQuotaExceeded) {
		return fmt.Sprintf("写入复制目标失败：磁盘空间或用户配额不足，源文件未直接覆盖目标（%s）", context)
	}
	if errors.Is(cause, syscall.EROFS) {
		return fmt.Sprintf("写入复制目标失败：目标文件系统为只读，源文件未直接覆盖目标（%s）", context)
	}
	if errors.Is(cause, syscall.EBUSY) {
		return fmt.Sprintf("写入复制目标失败：目标或所在文件系统正忙，请确认没有其他任务正在操作该路径（%s）", context)
	}
	if errors.Is(cause, syscall.EXDEV) {
		return fmt.Sprintf("提交复制结果失败：目标目录不支持与临时副本进行同文件系统原子切换，源文件和原目标应仍保留（%s）", context)
	}
	if phase == "target" {
		return fmt.Sprintf("复制失败：写入临时副本或提交目标时发生文件系统错误，原目标不会在复制未完成时主动删除（%s）", context)
	}
	return fmt.Sprintf("读取复制源失败：读取、遍历或校验源内容时发生文件系统错误（%s）", context)
}

func MoveFileOrDir(c *gin.Context) {
	var input transferInput
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "移动文件或目录参数格式不正确")
		return
	}
	targetDir, targetName := splitTargetPath(input.TargetPath)
	startFileOperation(c, "file.move", operationAuditPath(input.SourcePath, targetDir, targetName))
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	result, err := manager.Move(input.SourcePath, targetDir, targetName)
	if err != nil {
		handleFileError(c, err, "移动失败")
		return
	}
	core.HandleSuccess(c, result)
	finishFileOperation(c, "success", fmt.Sprintf("移动 %d 项", result.Entries))
}

func RenameFileOrDir(c *gin.Context) {
	var input struct {
		Path    string `json:"path" binding:"required"`
		NewName string `json:"newName" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "重命名文件或目录参数格式不正确")
		return
	}
	startFileOperation(c, "file.rename", operationAuditPath(input.Path, "", input.NewName))
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	result, err := manager.Rename(input.Path, input.NewName)
	if err != nil {
		handleFileError(c, err, "重命名失败")
		return
	}
	core.HandleSuccess(c, result)
	finishFileOperation(c, "success", "重命名为 "+input.NewName)
}

type archiveTaskInput struct {
	Path        string `json:"path" binding:"required"`
	TargetDir   string `json:"targetDir" binding:"required"`
	ArchiveName string `json:"archiveName" binding:"required"`
}

type extractTaskInput struct {
	Path      string `json:"path" binding:"required"`
	TargetDir string `json:"targetDir"`
	Overwrite bool   `json:"overwrite"`
}

func ArchiveFileOrDir(c *gin.Context) {
	var input archiveTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "归档文件或目录参数格式不正确")
		return
	}
	startFileOperation(c, "file.archive", operationAuditPath(input.Path, input.TargetDir, input.ArchiveName))
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	if _, _, err := manager.Stat(input.Path); err != nil {
		handleFileError(c, err, "读取压缩源失败")
		return
	}
	targetInfo, _, err := manager.Stat(input.TargetDir)
	if err != nil {
		handleFileError(c, err, "读取压缩目标目录失败")
		return
	}
	if !targetInfo.IsDir() {
		handleFileError(c, fmt.Errorf("archive target is not a directory"), "压缩目标必须是目录")
		return
	}
	// The protected route always establishes the authenticated user. Keeping a
	// zero value here also lets trusted in-process callers submit a task.
	userID, _ := middleware.AuthenticatedUserID(c)
	settings := currentFileSettings()
	task, err := submitArchiveTask(input, userID, manager.RootPath(), settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "创建归档任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
		"taskId": task.ID, "status": task.Status, "statusUrl": "/v1/ftp/archive/tasks/" + task.ID,
	}))
	finishFileOperation(c, "success", "归档任务已提交")
}

func ExtractFile(c *gin.Context) {
	var input extractTaskInput
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "解压参数格式不正确")
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	probe, err := manager.ProbeArchive(input.Path)
	if err != nil {
		_, message := extractTaskFailure(err)
		handleBadRequest(c, err, message)
		return
	}
	if strings.TrimSpace(input.TargetDir) == "" {
		sourceRelative, relativeErr := manager.Relative(input.Path)
		if relativeErr != nil {
			handleFileError(c, relativeErr, "解压源路径无效")
			return
		}
		parent := pathpkg.Dir(sourceRelative)
		targetRelative := probe.BaseName
		if parent != "." {
			targetRelative = pathpkg.Join(parent, probe.BaseName)
		}
		input.TargetDir = manager.VirtualPath(targetRelative)
	} else if _, err := manager.Relative(input.TargetDir); err != nil {
		handleFileError(c, err, "解压目标路径无效")
		return
	}
	startFileOperation(c, "file.extract", strings.TrimSpace(input.Path)+" -> "+strings.TrimSpace(input.TargetDir))
	userID, _ := middleware.AuthenticatedUserID(c)
	settings := currentFileSettings()
	task, err := submitExtractTask(input, userID, manager.RootPath(), probe.Format, settings)
	if err != nil {
		handleFileError(c, err, "创建解压任务失败")
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{
		"taskId": task.ID, "status": task.Status, "targetDir": task.TargetDir,
		"archiveFormat": task.ArchiveFormat, "statusUrl": "/v1/ftp/extract/tasks/" + task.ID,
	}))
	finishFileOperation(c, "success", "解压任务已提交")
}

func GetFileProperties(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "读取文件属性参数格式不正确")
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	info, relative, err := manager.Stat(input.Path)
	if err != nil {
		handleFileError(c, err, "读取文件属性失败")
		return
	}
	lstat, lstatErr := manager.LstatRelative(relative)
	isSymlink := lstatErr == nil && lstat.Mode()&os.ModeSymlink != 0
	userName, _, uid, _ := fileOwner(info)
	fileType := "file"
	if info.IsDir() {
		fileType = "directory"
	} else if isSymlink {
		fileType = "symlink"
	}
	mimeType := ""
	if !info.IsDir() {
		mimeType = mime.TypeByExtension(pathpkg.Ext(info.Name()))
	}
	canEdit := hasFileEditPermission(c) && !info.IsDir() && !isSymlink && canEditFile(manager, input.Path, currentFileSettings().editMaxBytes)
	archiveInfo := filemanager.InspectArchiveName(info.Name())
	isArchive := !info.IsDir() && !isSymlink && archiveInfo.Format != ""
	canExtract := false
	if access, ok := middleware.UserAccess(c); ok {
		canExtract = access.HasPermission(accessservice.PermissionFileArchive)
	}
	core.HandleSuccess(c, gin.H{
		"path":          manager.VirtualPath(relative),
		"name":          info.Name(),
		"type":          fileType,
		"isDir":         info.IsDir(),
		"isSymlink":     isSymlink,
		"permissions":   fmt.Sprintf("%04o", info.Mode().Perm()),
		"owner":         userName,
		"uid":           uid,
		"size":          info.Size(),
		"mimeType":      mimeType,
		"modTime":       info.ModTime().Format(time.RFC3339),
		"canEdit":       canEdit,
		"isArchive":     isArchive,
		"archiveFormat": archiveInfo.Format,
		"canExtract":    canExtract && isArchive && archiveInfo.Supported,
	})
}

func operationAuditPath(source, targetDir, targetName string) string {
	source = strings.TrimSpace(source)
	targetDir = strings.TrimSpace(targetDir)
	targetName = strings.TrimSpace(targetName)
	if targetDir == "" {
		if targetName == "" {
			return source
		}
		return source + " -> " + targetName
	}
	if targetName == "" {
		targetName = pathpkg.Base(source)
	}
	return source + " -> " + pathpkg.Join(targetDir, targetName)
}

func splitTargetPath(targetPath string) (string, string) {
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		return "", ""
	}
	return pathpkg.Dir(targetPath), pathpkg.Base(targetPath)
}
