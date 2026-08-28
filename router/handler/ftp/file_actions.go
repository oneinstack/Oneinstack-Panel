package ftp

import (
	"fmt"
	"mime"
	"net/http"
	"os"
	pathpkg "path"
	"strings"
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
		handleFileError(c, err, "读取复制源失败")
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
		handleFileError(c, err, "复制失败")
		return
	}
	core.HandleSuccess(c, result)
	finishFileOperation(c, "success", fmt.Sprintf("复制 %d 项，共 %d 字节", result.Entries, result.Bytes))
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
