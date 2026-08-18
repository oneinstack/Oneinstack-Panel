package ftp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"oneinstack/app"
	"oneinstack/core"
	accessservice "oneinstack/internal/services/access"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"
	"oneinstack/utils"
	"os"
	"os/user"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxTreeDepth   = 5
	maxTreeEntries = 1000
)

const unsupportedFilePreviewEditMessage = "此文件不支持预览/编辑"

var remoteDownloader = filemanager.NewDownloader()

type FileDetail struct {
	Path       string `json:"path"`
	Name       string `json:"name"`
	User       string `json:"user"`
	Group      string `json:"group"`
	UID        string `json:"uid"`
	GID        string `json:"gid"`
	Extension  string `json:"extension"`
	Content    string `json:"content"`
	Size       int64  `json:"size"`
	IsDir      bool   `json:"isDir"`
	IsSymlink  bool   `json:"isSymlink"`
	IsHidden   bool   `json:"isHidden"`
	LinkPath   string `json:"linkPath"`
	Type       string `json:"type"`
	Mode       string `json:"mode"`
	MimeType   string `json:"mimeType"`
	UpdateTime string `json:"updateTime"`
	ModTime    string `json:"modTime"`
	Items      any    `json:"items"`
	ItemTotal  int    `json:"itemTotal"`
	FavoriteID int    `json:"favoriteID"`
	IsDetail   bool   `json:"isDetail"`
	Revision   string `json:"revision"`
	CanEdit    bool   `json:"canEdit"`
}

type FileNode struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	IsDir     bool        `json:"isDir"`
	Extension string      `json:"extension"`
	CanEdit   bool        `json:"canEdit"`
	Children  []*FileNode `json:"children,omitempty"`
}

func ListDirectory(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件列表参数格式不正确")
		return
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	entries, relative, err := manager.ReadDir(input.Path)
	if err != nil {
		handleFileError(c, err, "读取目录失败")
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	fileInfos := make([]gin.H, 0, len(entries))
	favoritePaths := make([]string, 0, len(entries))
	canDelete := false
	if access, ok := middleware.UserAccess(c); ok {
		canDelete = access.HasPermission(accessservice.PermissionFileDelete)
	}
	for _, entry := range entries {
		childRelative := pathpkg.Join(relative, entry.Name())
		if relative == "." {
			childRelative = entry.Name()
		}
		childPath := manager.VirtualPath(childRelative)
		favoritePaths = append(favoritePaths, childPath)
	}
	userID, _ := middleware.AuthenticatedUserID(c)
	favoriteMap := favoriteMapForPaths(userID, favoritePaths)
	canEditPermission := hasFileEditPermission(c)
	settings := currentFileSettings()
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			handleFileError(c, err, "读取文件信息失败")
			return
		}
		userName, groupName, _, _ := fileOwner(info)
		childRelative := pathpkg.Join(relative, entry.Name())
		if relative == "." {
			childRelative = entry.Name()
		}
		childPath := manager.VirtualPath(childRelative)
		isSymlink := info.Mode()&os.ModeSymlink != 0
		isDir := entry.IsDir()
		if isSymlink {
			if targetInfo, _, targetErr := manager.Stat(childPath); targetErr == nil {
				isDir = targetInfo.IsDir()
			}
		}
		canEdit := canEditPermission && !isDir && !isSymlink && canEditFile(manager, childPath, settings.editMaxBytes)
		fileInfos = append(fileInfos, gin.H{
			"path":        childPath,
			"name":        entry.Name(),
			"isDir":       isDir,
			"isSymlink":   isSymlink,
			"permissions": fmt.Sprintf("%04o", info.Mode().Perm()),
			"user":        userName,
			"group":       groupName,
			"modTime":     info.ModTime().Format("2006-01-02 15:04:05"),
			"size":        utils.FormatBytes(info.Size()),
			"favoriteID":  favoriteMap[childPath],
			"canDelete":   canDelete,
			"canEdit":     canEdit,
		})
	}
	core.HandleSuccess(c, gin.H{"files": fileInfos, "path": manager.VirtualPath(relative)})
}

func CreateFileOrDir(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
		Type string `json:"type" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "创建文件或目录参数格式不正确")
		return
	}
	startFileOperation(c, "file.create", input.Path)

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	relative, err := manager.Relative(input.Path)
	if err != nil {
		handleFileError(c, err, "路径无效")
		return
	}
	if relative == "." {
		handleFileError(c, filemanager.ErrRootOperation, "不能覆盖文件根目录")
		return
	}

	switch input.Type {
	case "file":
		err = manager.CreateFile(input.Path, 0644)
	case "dir":
		err = manager.MkdirAll(input.Path, 0755)
	default:
		err = fmt.Errorf("%w: type must be file or dir", filemanager.ErrInvalidPath)
	}
	if err != nil {
		handleFileError(c, err, "创建文件或目录失败")
		return
	}
	core.HandleSuccess(c, "创建成功")
	finishFileOperation(c, "success", "创建"+map[bool]string{true: "目录", false: "文件"}[input.Type == "dir"])
}

func UploadFile(c *gin.Context) {
	settings := currentFileSettings()
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, settings.uploadMaxBytes+(2<<20))
	fileHeader, err := c.FormFile("file")
	if err != nil {
		handleBadRequest(c, err, "读取上传文件失败")
		return
	}
	if fileHeader.Size < 0 || fileHeader.Size > settings.uploadMaxBytes {
		handleFileError(c, fmt.Errorf("%w: upload exceeds %d bytes", filemanager.ErrInvalidPath, settings.uploadMaxBytes), "上传文件过大")
		return
	}
	if err := filemanager.ValidateName(fileHeader.Filename); err != nil {
		handleFileError(c, err, "文件名无效")
		return
	}

	virtualDir := c.PostForm("path")
	if strings.TrimSpace(virtualDir) == "" {
		virtualDir = "/"
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	target, err := manager.Join(virtualDir, fileHeader.Filename)
	if err != nil {
		handleFileError(c, err, "上传路径无效")
		return
	}
	startFileOperation(c, "file.upload", manager.VirtualPath(target))
	reservation, _, err := manager.ReserveCapacity(fileHeader.Size, settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "存储容量不足")
		return
	}
	defer reservation.Release()
	source, err := fileHeader.Open()
	if err != nil {
		handleFileError(c, err, "打开上传文件失败")
		return
	}
	defer source.Close()

	destination, err := manager.OpenFileRelative(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		handleFileError(c, err, "创建目标文件失败")
		return
	}

	keepFile := false
	defer func() {
		_ = destination.Close()
		if !keepFile {
			_ = manager.RemoveAll(manager.VirtualPath(target))
		}
	}()
	written, err := io.Copy(destination, io.LimitReader(source, settings.uploadMaxBytes+1))
	if err != nil {
		handleFileError(c, err, "保存上传文件失败")
		return
	}
	if written > settings.uploadMaxBytes {
		handleFileError(c, fmt.Errorf("%w: upload exceeds %d bytes", filemanager.ErrInvalidPath, settings.uploadMaxBytes), "上传文件过大")
		return
	}
	if err := destination.Sync(); err != nil {
		handleFileError(c, err, "同步上传文件失败")
		return
	}
	if err := destination.Close(); err != nil {
		handleFileError(c, err, "关闭上传文件失败")
		return
	}
	keepFile = true
	core.HandleSuccess(c, gin.H{"path": manager.VirtualPath(target), "size": written})
	finishFileOperation(c, "success", fmt.Sprintf("上传 %d 字节", written))
}

func DownloadFile(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件下载参数格式不正确")
		return
	}
	startFileOperation(c, "file.download", input.Path)

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	file, _, err := manager.Open(input.Path)
	if err != nil {
		handleFileError(c, err, "打开下载文件失败")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		handleFileError(c, err, "读取下载文件信息失败")
		return
	}
	if !info.Mode().IsRegular() {
		handleFileError(c, filemanager.ErrNotRegular, "仅支持下载普通文件")
		return
	}

	if contentType := mime.TypeByExtension(pathpkg.Ext(info.Name())); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Header("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(info.Name()))
	c.Header("X-Content-Type-Options", "nosniff")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
	finishFileOperation(c, "success", fmt.Sprintf("下载 %d 字节", info.Size()))
}

func DeleteFileOrDir(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "删除文件或目录参数格式不正确")
		return
	}
	startFileOperation(c, "file.trash", input.Path)

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	deletedBy := ""
	if value, exists := c.Get("username"); exists {
		deletedBy, _ = value.(string)
	}
	entry, err := manager.MoveToTrash(input.Path, deletedBy)
	if err != nil {
		handleFileError(c, err, "删除文件或目录失败")
		return
	}
	core.HandleSuccess(c, entry)
	finishFileOperation(c, "success", "移入回收站")
}

func ListTrash(c *gin.Context) {
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	entries, err := manager.ListTrash()
	if err != nil {
		handleFileError(c, err, "读取回收站失败")
		return
	}
	core.HandleSuccess(c, gin.H{"items": entries, "total": len(entries)})
}

func RestoreTrash(c *gin.Context) {
	var input struct {
		ID string `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "恢复回收站文件参数格式不正确")
		return
	}
	startFileOperation(c, "file.restore", "/trash/"+input.ID)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	entry, err := manager.RestoreTrash(input.ID)
	if err != nil {
		handleFileError(c, err, "从回收站恢复文件或目录失败")
		return
	}
	startFileOperation(c, "file.restore", entry.OriginalPath)
	core.HandleSuccess(c, entry)
	finishFileOperation(c, "success", "从回收站恢复")
}

func DeleteTrashPermanently(c *gin.Context) {
	var input struct {
		ID string `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "彻底删除回收站文件参数格式不正确")
		return
	}
	startFileOperation(c, "file.delete_permanently", "/trash/"+input.ID)
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	if err := manager.DeleteTrashPermanently(input.ID); err != nil {
		handleFileError(c, err, "彻底删除失败")
		return
	}
	core.HandleSuccess(c, "彻底删除成功")
	finishFileOperation(c, "success", "彻底删除回收站文件")
}

func EmptyTrash(c *gin.Context) {
	var input struct {
		Confirm bool `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !input.Confirm {
		if err == nil {
			err = errors.New("必须确认清空回收站")
		}
		handleBadRequest(c, err, "清空回收站参数格式不正确")
		return
	}
	startFileOperation(c, "file.empty_trash", "/")
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	deleted, err := manager.EmptyTrash()
	if err != nil {
		handleFileError(c, err, "清空回收站失败")
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": deleted})
	finishFileOperation(c, "success", fmt.Sprintf("清空回收站，共 %d 项", deleted))
}

func ModifyFileOrDirAttributes(c *gin.Context) {
	var input struct {
		Path      string `json:"path" binding:"required"`
		Perm      string `json:"perm" binding:"required"`
		User      string `json:"user" binding:"required"`
		Group     string `json:"group" binding:"required"`
		Recursive bool   `json:"recursive"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件属性修改参数格式不正确")
		return
	}
	startFileOperation(c, "file.attributes", input.Path)

	permission, err := strconv.ParseUint(input.Perm, 8, 32)
	if err != nil || permission > 0777 {
		handleFileError(c, fmt.Errorf("%w: permission must be between 0000 and 0777", filemanager.ErrInvalidPath), "权限格式无效")
		return
	}
	uid, err := lookupUserID(input.User)
	if err != nil {
		handleFileError(c, fmt.Errorf("%w: unknown user", filemanager.ErrInvalidName), "用户无效")
		return
	}
	gid, err := lookupGroupID(input.Group)
	if err != nil {
		handleFileError(c, fmt.Errorf("%w: unknown group", filemanager.ErrInvalidName), "用户组无效")
		return
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	relative, err := manager.Relative(input.Path)
	if err != nil {
		handleFileError(c, err, "路径无效")
		return
	}
	if relative == "." {
		handleFileError(c, filemanager.ErrRootOperation, "不能修改文件根目录属性")
		return
	}
	info, err := manager.LstatRelative(relative)
	if err != nil {
		handleFileError(c, err, "读取文件信息失败")
		return
	}
	if info.Mode()&os.ModeSymlink != 0 {
		handleFileError(c, filemanager.ErrInvalidPath, "不能修改符号链接属性")
		return
	}

	modify := func(path string) error {
		info, err := manager.LstatRelative(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		file, err := manager.OpenRelative(path)
		if err != nil {
			return err
		}
		defer file.Close()
		if err := file.Chmod(os.FileMode(permission)); err != nil {
			return err
		}
		return file.Chown(uid, gid)
	}

	if input.Recursive {
		err = manager.Walk(input.Path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			return modify(path)
		})
	} else {
		err = modify(relative)
	}
	if err != nil {
		handleFileError(c, err, "修改属性失败")
		return
	}
	core.HandleSuccess(c, "修改成功")
	finishFileOperation(c, "success", "修改权限和所有者")
}

func Content(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "读取文件内容参数格式不正确")
		return
	}
	startFileOperation(c, "file.read", input.Path)

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	settings := currentFileSettings()
	info, data, relative, err := readEditableFile(manager, input.Path, settings.editMaxBytes)
	if err != nil {
		handleFileError(c, err, editableFileErrorMessage(err, "读取文件失败"))
		return
	}

	userName, groupName, uid, gid := fileOwner(info)
	mimeType := mime.TypeByExtension(pathpkg.Ext(info.Name()))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	core.HandleSuccess(c, FileDetail{
		Path:       manager.VirtualPath(relative),
		Name:       info.Name(),
		User:       userName,
		Group:      groupName,
		UID:        uid,
		GID:        gid,
		Extension:  pathpkg.Ext(info.Name()),
		Content:    string(data),
		Size:       info.Size(),
		IsDir:      false,
		IsSymlink:  false,
		IsHidden:   strings.HasPrefix(info.Name(), "."),
		Mode:       fmt.Sprintf("%04o", info.Mode().Perm()),
		MimeType:   mimeType,
		ModTime:    info.ModTime().Format(time.RFC3339Nano),
		FavoriteID: favoriteIDForPath(c, manager.VirtualPath(relative)),
		Revision:   contentRevision(data),
		CanEdit:    hasFileEditPermission(c),
	})
	finishFileOperation(c, "success", fmt.Sprintf("读取 %d 字节", info.Size()))
}

func GetDirectoryTreeHandler(c *gin.Context) {
	var input struct {
		Path         string `json:"path" binding:"required"`
		ShowHidden   bool   `json:"showHidden"`
		DirOnly      bool   `json:"dirOnly"`
		ContainSub   *bool  `json:"containSub"`
		MaxDepth     int    `json:"maxDepth"`
		MaxPerFolder int    `json:"maxPerFolder"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "读取目录树参数格式不正确")
		return
	}
	if input.MaxDepth <= 0 {
		input.MaxDepth = 4
	}
	if input.MaxDepth > maxTreeDepth {
		input.MaxDepth = maxTreeDepth
	}
	if input.MaxPerFolder <= 0 || input.MaxPerFolder > maxTreeEntries {
		input.MaxPerFolder = maxTreeEntries
	}
	containSub := true
	if input.ContainSub != nil {
		containSub = *input.ContainSub
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	info, relative, err := manager.Stat(input.Path)
	if err != nil {
		handleFileError(c, err, "读取目录树失败")
		return
	}
	if !info.IsDir() {
		handleFileError(c, filemanager.ErrInvalidPath, "路径不是目录")
		return
	}

	children, err := scanTree(manager, manager.VirtualPath(relative), input.ShowHidden, input.DirOnly, containSub, input.MaxDepth, input.MaxPerFolder, 1, hasFileEditPermission(c), currentFileSettings().editMaxBytes)
	if err != nil {
		handleFileError(c, err, "读取目录树失败")
		return
	}
	virtualPath := manager.VirtualPath(relative)
	name := pathpkg.Base(virtualPath)
	if virtualPath == "/" {
		name = "/"
	}
	root := &FileNode{
		ID:       virtualPath,
		Name:     name,
		Path:     virtualPath,
		IsDir:    true,
		Children: children,
	}
	core.HandleSuccess(c, []*FileNode{root})
}

func SaveFile(c *gin.Context) {
	var input struct {
		Path     string `json:"path" binding:"required"`
		Content  string `json:"content"`
		Revision string `json:"revision"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "保存文件参数格式不正确")
		return
	}
	startFileOperation(c, "file.save", input.Path)
	settings := currentFileSettings()
	if int64(len(input.Content)) > settings.editMaxBytes {
		handleFileError(c, filemanager.ErrUnsupportedType, unsupportedFilePreviewEditMessage)
		return
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	info, currentData, _, err := readEditableFile(manager, input.Path, settings.editMaxBytes)
	if err != nil {
		handleFileError(c, err, editableFileErrorMessage(err, "读取文件信息失败"))
		return
	}
	if strings.TrimSpace(input.Revision) != "" {
		if !strings.EqualFold(contentRevision(currentData), strings.TrimSpace(input.Revision)) {
			handleFileError(c, filemanager.ErrRevisionConflict, "文件已被其他进程修改，请重新读取")
			return
		}
	}
	additionalBytes := int64(len(input.Content)) - info.Size()
	if additionalBytes < 0 {
		additionalBytes = 0
	}
	reservation, _, err := manager.ReserveCapacity(additionalBytes, settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "存储容量不足")
		return
	}
	defer reservation.Release()
	if err := manager.WriteExistingFile(input.Path, []byte(input.Content)); err != nil {
		handleFileError(c, err, "保存文件内容失败")
		return
	}
	revision := contentRevision([]byte(input.Content))
	core.HandleSuccess(c, gin.H{"message": "保存成功", "revision": revision})
	finishFileOperation(c, "success", fmt.Sprintf("保存 %d 字节", len(input.Content)))
}

func readEditableFile(manager *filemanager.Manager, virtualPath string, maxBytes int64) (os.FileInfo, []byte, string, error) {
	relative, err := manager.Relative(virtualPath)
	if err != nil {
		return nil, nil, "", err
	}
	lstat, err := manager.LstatRelative(relative)
	if err != nil {
		return nil, nil, "", err
	}
	if lstat.Mode()&os.ModeSymlink != 0 {
		return nil, nil, "", filemanager.ErrUnsupportedType
	}

	file, _, err := manager.Open(virtualPath)
	if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, nil, "", filemanager.ErrUnsupportedType
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, nil, "", err
	}
	if int64(len(data)) > maxBytes || bytes.IndexByte(data, 0) >= 0 {
		return nil, nil, "", filemanager.ErrUnsupportedType
	}
	return info, data, relative, nil
}

func canEditFile(manager *filemanager.Manager, virtualPath string, maxBytes int64) bool {
	_, _, _, err := readEditableFile(manager, virtualPath, maxBytes)
	return err == nil
}

func hasFileEditPermission(c *gin.Context) bool {
	access, ok := middleware.UserAccess(c)
	return ok && access.HasPermission(accessservice.PermissionFileEdit)
}

func editableFileErrorMessage(err error, fallback string) string {
	if errors.Is(err, filemanager.ErrUnsupportedType) {
		return unsupportedFilePreviewEditMessage
	}
	return fallback
}

func UrlDownloadFile(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
		URL  string `json:"url" binding:"required"`
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "文件操作参数格式不正确")
		return
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()
	target, err := manager.Join(input.Path, input.Name)
	if err != nil {
		handleFileError(c, err, "远程下载路径无效")
		return
	}
	startFileOperation(c, "file.remote_download", manager.VirtualPath(target))

	settings := currentFileSettings()
	reservation, _, err := manager.ReserveCapacity(settings.uploadMaxBytes, settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "存储容量不足")
		return
	}
	defer reservation.Release()
	if err := remoteDownloader.Download(c.Request.Context(), manager, input.URL, input.Path, input.Name, settings.uploadMaxBytes); err != nil {
		handleFileError(c, err, "远程下载失败")
		return
	}
	core.HandleSuccess(c, gin.H{"path": manager.VirtualPath(target)})
	finishFileOperation(c, "success", "远程下载完成")
}

func Capacity(c *gin.Context) {
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	settings := currentFileSettings()
	status, err := manager.Capacity(settings.capacityPolicy)
	if err != nil {
		handleFileError(c, err, "读取存储容量失败")
		return
	}
	core.HandleSuccess(c, gin.H{
		"rootPath":             manager.RootPath(),
		"capacity":             status,
		"uploadMaxBytes":       settings.uploadMaxBytes,
		"editMaxBytes":         settings.editMaxBytes,
		"trashRetentionDays":   app.ONE_CONFIG.System.TrashRetentionDays,
		"trashCleanupSchedule": app.ONE_CONFIG.System.TrashCleanupSchedule,
	})
}

func scanTree(
	manager *filemanager.Manager,
	virtualPath string,
	showHidden bool,
	dirOnly bool,
	containSub bool,
	maxDepth int,
	maxPerFolder int,
	level int,
	canEditPermission bool,
	editMaxBytes int64,
) ([]*FileNode, error) {
	if level > maxDepth {
		return nil, nil
	}
	entries, relative, err := manager.ReadDir(virtualPath)
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})

	nodes := make([]*FileNode, 0, min(len(entries), maxPerFolder))
	for _, entry := range entries {
		if len(nodes) >= maxPerFolder {
			break
		}
		if !showHidden && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		isDir := entry.IsDir()
		if dirOnly && !isDir {
			continue
		}

		childRelative := pathpkg.Join(relative, entry.Name())
		if relative == "." {
			childRelative = entry.Name()
		}
		childPath := manager.VirtualPath(childRelative)
		node := &FileNode{
			ID:        childPath,
			Name:      entry.Name(),
			Path:      childPath,
			IsDir:     isDir,
			Extension: pathpkg.Ext(entry.Name()),
			CanEdit:   canEditPermission && !isDir && canEditFile(manager, childPath, editMaxBytes),
		}
		if isDir && containSub && level < maxDepth {
			node.Children, err = scanTree(manager, childPath, showHidden, dirOnly, true, maxDepth, maxPerFolder, level+1, canEditPermission, editMaxBytes)
			if err != nil {
				return nil, err
			}
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func managerForRequest(c *gin.Context) (*filemanager.Manager, bool) {
	rootPath := strings.TrimSpace(app.ONE_CONFIG.System.DefaultPath)
	if rootPath == "" {
		core.HandleError(c, core.NewError(core.ErrConfigError, "未配置文件管理根目录"))
		return nil, false
	}
	manager, err := filemanager.New(rootPath)
	if err != nil {
		if fallbackRoot, ok := developmentFileRootFallback(rootPath); ok {
			manager, err = filemanager.New(fallbackRoot)
		}
	}
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "文件服务初始化失败"))
		return nil, false
	}
	manager.WithProtectedPaths([]string{
		filepath.Join(app.GetBasePath(), ".ssh"),
	})

	access, accessOk := middleware.UserAccess(c)
	if accessOk && !access.HasPermission(accessservice.PermissionFileScopeRoot) {
		var allowedPrefixes []string
		if access.HasPermission(accessservice.PermissionFileScopeWebsites) {
			allowedPrefixes = append(allowedPrefixes, "/websites")
		}
		if access.HasPermission(accessservice.PermissionFileScopeBackups) {
			allowedPrefixes = append(allowedPrefixes, "/backups")
		}
		if len(allowedPrefixes) > 0 {
			manager.WithScope(allowedPrefixes)
		}
	}
	return manager, true
}

func developmentFileRootFallback(rootPath string) (string, bool) {
	if os.Getenv("GO_ENV") != "development" && app.ENV != "debug" {
		return "", false
	}
	if filepath.Clean(rootPath) != "/data" {
		return "", false
	}
	return filepath.Join(app.GetBasePath(), "file-root"), true
}

func handleFileError(c *gin.Context, err error, message string) {
	category := fileErrorCategory(err)
	requestID, _ := c.Get(middleware.ContextRequestID)
	action, _ := c.Get(fileOperationActionKey)
	log.Printf(
		"file operation failed request_id=%s action=%s category=%s errno=%s",
		valueString(requestID), valueString(action), category, fileErrorErrno(err),
	)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		core.HandleError(c, core.WrapError(err, core.ErrFileNotFound, message))
	case errors.Is(err, fs.ErrPermission):
		core.HandleError(c, core.WrapError(err, core.ErrPermissionDenied, message))
	case errors.Is(err, filemanager.ErrQuotaExceeded),
		errors.Is(err, filemanager.ErrInsufficientSpace),
		errors.Is(err, syscall.ENOSPC),
		errors.Is(err, syscall.EDQUOT):
		core.HandleError(c, core.WrapError(err, core.ErrInsufficientStorage, message))
	case errors.Is(err, syscall.EBUSY):
		core.HandleError(c, core.WrapError(err, core.ErrConflict, message))
	case errors.Is(err, syscall.EROFS):
		core.HandleError(c, core.WrapError(err, core.ErrPermissionDenied, message))
	case errors.Is(err, filemanager.ErrRootOperation):
		core.HandleError(c, core.WrapError(err, core.ErrForbidden, message))
	case errors.Is(err, filemanager.ErrRevisionConflict):
		core.HandleError(c, core.WrapError(err, core.ErrConflict, message))
	case errors.Is(err, filemanager.ErrInvalidPath),
		errors.Is(err, filemanager.ErrInvalidName),
		errors.Is(err, filemanager.ErrNotRegular),
		errors.Is(err, filemanager.ErrUnsupportedType),
		errors.Is(err, filemanager.ErrReservedPath),
		errors.Is(err, filemanager.ErrUnsafeRemoteURL),
		errors.Is(err, filemanager.ErrDownloadLimit),
		errors.Is(err, fs.ErrExist):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
	default:
		core.HandleError(c, core.NewError(core.ErrInternalError, message))
	}
	finishFileOperation(c, "failure", message+"（"+category+"）")
}

func fileErrorCategory(err error) string {
	switch {
	case errors.Is(err, syscall.EXDEV):
		return "跨挂载点移动失败"
	case errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return "存储空间不足"
	case errors.Is(err, syscall.EBUSY):
		return "文件正在使用"
	case errors.Is(err, syscall.EROFS):
		return "文件系统只读"
	case errors.Is(err, fs.ErrPermission):
		return "权限不足"
	case errors.Is(err, fs.ErrNotExist):
		return "文件不存在"
	case errors.Is(err, filemanager.ErrUnsupportedType):
		return "文件类型不支持"
	default:
		return "文件系统错误"
	}
}

func fileErrorErrno(err error) string {
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno.Error()
	}
	return "unknown"
}

func contentRevision(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

type fileSettings struct {
	uploadMaxBytes int64
	editMaxBytes   int64
	capacityPolicy filemanager.CapacityPolicy
}

func currentFileSettings() fileSettings {
	uploadMaxBytes := app.ONE_CONFIG.System.FileUploadMaxBytes
	if uploadMaxBytes <= 0 {
		uploadMaxBytes = filemanager.DefaultRemoteDownloadLimit
	}
	editMaxBytes := app.ONE_CONFIG.System.FileEditMaxBytes
	if editMaxBytes <= 0 {
		editMaxBytes = 10 << 20
	}
	return fileSettings{
		uploadMaxBytes: uploadMaxBytes,
		editMaxBytes:   editMaxBytes,
		capacityPolicy: filemanager.CapacityPolicy{
			QuotaBytes:   max(app.ONE_CONFIG.System.FileRootQuotaBytes, 0),
			MinFreeBytes: max(app.ONE_CONFIG.System.FileMinFreeBytes, 0),
		},
	}
}

func handleBadRequest(c *gin.Context, err error, message string) {
	core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
}

func fileOwner(info os.FileInfo) (userName, groupName, uid, gid string) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown", "unknown", "", ""
	}
	uid = strconv.FormatUint(uint64(stat.Uid), 10)
	gid = strconv.FormatUint(uint64(stat.Gid), 10)
	return getUserName(stat.Uid), getGroupName(stat.Gid), uid, gid
}

func lookupUserID(username string) (int, error) {
	account, err := user.Lookup(username)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(account.Uid)
}

func lookupGroupID(groupName string) (int, error) {
	group, err := user.LookupGroup(groupName)
	if err != nil {
		return -1, err
	}
	return strconv.Atoi(group.Gid)
}

func getUserName(uid uint32) string {
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return "unknown"
	}
	return account.Username
}

func getGroupName(gid uint32) string {
	group, err := user.LookupGroupId(strconv.FormatUint(uint64(gid), 10))
	if err != nil {
		return "unknown"
	}
	return group.Name
}
