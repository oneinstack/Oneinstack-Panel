package ftp

import (
	"fmt"
	"mime"
	"os"
	pathpkg "path"
	"strings"
	"time"

	"oneinstack/core"

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
		handleBadRequest(c, err, "请求参数错误")
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
		handleBadRequest(c, err, "请求参数错误")
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
		handleBadRequest(c, err, "请求参数错误")
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

func ArchiveFileOrDir(c *gin.Context) {
	var input struct {
		Path        string `json:"path" binding:"required"`
		TargetDir   string `json:"targetDir" binding:"required"`
		ArchiveName string `json:"archiveName" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	startFileOperation(c, "file.archive", operationAuditPath(input.Path, input.TargetDir, input.ArchiveName))
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	measured, err := manager.Measure(input.Path)
	if err != nil {
		handleFileError(c, err, "读取压缩源失败")
		return
	}
	settings := currentFileSettings()
	reservation, _, err := manager.ReserveCapacity(measured.Bytes, settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "创建压缩包所需存储容量不足")
		return
	}
	defer reservation.Release()
	result, err := manager.Archive(input.Path, input.TargetDir, input.ArchiveName)
	if err != nil {
		handleFileError(c, err, "创建压缩包失败")
		return
	}
	core.HandleSuccess(c, result)
	finishFileOperation(c, "success", fmt.Sprintf("压缩 %d 项，共 %d 字节", result.Entries, result.Bytes))
}

func GetFileProperties(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
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
	core.HandleSuccess(c, gin.H{
		"path":        manager.VirtualPath(relative),
		"name":        info.Name(),
		"type":        fileType,
		"isDir":       info.IsDir(),
		"isSymlink":   isSymlink,
		"permissions": fmt.Sprintf("%04o", info.Mode().Perm()),
		"owner":       userName,
		"uid":         uid,
		"size":        info.Size(),
		"mimeType":    mimeType,
		"modTime":     info.ModTime().Format(time.RFC3339),
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
