package ftp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/services/filemanager"
	"oneinstack/utils"
	"os"
	"os/user"
	pathpkg "path"
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
}

type FileNode struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	IsDir     bool        `json:"isDir"`
	Extension string      `json:"extension"`
	Children  []*FileNode `json:"children,omitempty"`
}

func ListDirectory(c *gin.Context) {
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
		fileInfos = append(fileInfos, gin.H{
			"path":        manager.VirtualPath(childRelative),
			"name":        entry.Name(),
			"isDir":       entry.IsDir(),
			"isSymlink":   info.Mode()&os.ModeSymlink != 0,
			"permissions": fmt.Sprintf("%04o", info.Mode().Perm()),
			"user":        userName,
			"group":       groupName,
			"modTime":     info.ModTime().Format("2006-01-02 15:04:05"),
			"size":        utils.FormatBytes(info.Size()),
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
		handleBadRequest(c, err, "请求参数错误")
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
		handleFileError(c, err, "创建失败")
		return
	}
	core.HandleSuccess(c, "创建成功")
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
}

func DownloadFile(c *gin.Context) {
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
}

func DeleteFileOrDir(c *gin.Context) {
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

	deletedBy := ""
	if value, exists := c.Get("username"); exists {
		deletedBy, _ = value.(string)
	}
	entry, err := manager.MoveToTrash(input.Path, deletedBy)
	if err != nil {
		handleFileError(c, err, "删除失败")
		return
	}
	core.HandleSuccess(c, entry)
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
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	entry, err := manager.RestoreTrash(input.ID)
	if err != nil {
		handleFileError(c, err, "恢复失败")
		return
	}
	core.HandleSuccess(c, entry)
}

func DeleteTrashPermanently(c *gin.Context) {
	var input struct {
		ID string `json:"id" binding:"required"`
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

	if err := manager.DeleteTrashPermanently(input.ID); err != nil {
		handleFileError(c, err, "彻底删除失败")
		return
	}
	core.HandleSuccess(c, "彻底删除成功")
}

func EmptyTrash(c *gin.Context) {
	var input struct {
		Confirm bool `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil || !input.Confirm {
		if err == nil {
			err = errors.New("必须确认清空回收站")
		}
		handleBadRequest(c, err, "请求参数错误")
		return
	}
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
		handleBadRequest(c, err, "请求参数错误")
		return
	}

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
}

func SearchFile(_ *gin.Context) {}

func Content(c *gin.Context) {
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

	file, relative, err := manager.Open(input.Path)
	if err != nil {
		handleFileError(c, err, "打开文件失败")
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		handleFileError(c, err, "读取文件信息失败")
		return
	}
	if !info.Mode().IsRegular() {
		handleFileError(c, filemanager.ErrNotRegular, "仅支持读取普通文件")
		return
	}
	settings := currentFileSettings()
	if info.Size() > settings.editMaxBytes {
		handleFileError(c, fmt.Errorf("%w: editable file exceeds %d bytes", filemanager.ErrInvalidPath, settings.editMaxBytes), "文件过大，无法在线编辑")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, settings.editMaxBytes+1))
	if err != nil {
		handleFileError(c, err, "读取文件失败")
		return
	}
	if int64(len(data)) > settings.editMaxBytes {
		handleFileError(c, filemanager.ErrInvalidPath, "文件过大，无法在线编辑")
		return
	}

	userName, groupName, uid, gid := fileOwner(info)
	lstat, lstatErr := manager.LstatRelative(relative)
	isSymlink := lstatErr == nil && lstat.Mode()&os.ModeSymlink != 0
	mimeType := mime.TypeByExtension(pathpkg.Ext(info.Name()))
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
	}

	core.HandleSuccess(c, FileDetail{
		Path:      manager.VirtualPath(relative),
		Name:      info.Name(),
		User:      userName,
		Group:     groupName,
		UID:       uid,
		GID:       gid,
		Extension: pathpkg.Ext(info.Name()),
		Content:   string(data),
		Size:      info.Size(),
		IsDir:     false,
		IsSymlink: isSymlink,
		IsHidden:  strings.HasPrefix(info.Name(), "."),
		Mode:      fmt.Sprintf("%04o", info.Mode().Perm()),
		MimeType:  mimeType,
		ModTime:   info.ModTime().Format(time.RFC3339Nano),
	})
}

func GetDirectoryTreeHandler(c *gin.Context) {
	var input struct {
		Path         string `json:"path" binding:"required"`
		ShowHidden   bool   `json:"showHidden"`
		DirOnly      bool   `json:"dirOnly"`
		ContainSub   bool   `json:"containSub"`
		MaxDepth     int    `json:"maxDepth"`
		MaxPerFolder int    `json:"maxPerFolder"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	if input.MaxDepth <= 0 {
		input.MaxDepth = 2
	}
	if input.MaxDepth > maxTreeDepth {
		input.MaxDepth = maxTreeDepth
	}
	if input.MaxPerFolder <= 0 || input.MaxPerFolder > maxTreeEntries {
		input.MaxPerFolder = maxTreeEntries
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

	children, err := scanTree(manager, manager.VirtualPath(relative), input.ShowHidden, input.DirOnly, input.ContainSub, input.MaxDepth, input.MaxPerFolder, 1)
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
		Path    string `json:"path" binding:"required"`
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		handleBadRequest(c, err, "请求参数错误")
		return
	}
	settings := currentFileSettings()
	if int64(len(input.Content)) > settings.editMaxBytes {
		handleFileError(c, filemanager.ErrInvalidPath, "文件内容超过在线编辑限制")
		return
	}

	manager, ok := managerForRequest(c)
	if !ok {
		return
	}
	defer manager.Close()

	info, _, err := manager.Stat(input.Path)
	if err != nil {
		handleFileError(c, err, "读取文件信息失败")
		return
	}
	if !info.Mode().IsRegular() {
		handleFileError(c, filemanager.ErrNotRegular, "仅支持保存普通文件")
		return
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
		handleFileError(c, err, "保存失败")
		return
	}
	core.HandleSuccess(c, "保存成功")
}

func UrlDownloadFile(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
		URL  string `json:"url" binding:"required"`
		Name string `json:"name" binding:"required"`
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
	target, _ := manager.Join(input.Path, input.Name)
	core.HandleSuccess(c, gin.H{"path": manager.VirtualPath(target)})
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
		}
		if isDir && containSub && level < maxDepth {
			node.Children, err = scanTree(manager, childPath, showHidden, dirOnly, true, maxDepth, maxPerFolder, level+1)
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
		core.HandleError(c, core.NewError(core.ErrInternalError, "文件服务初始化失败"))
		return nil, false
	}
	return manager, true
}

func handleFileError(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		core.HandleError(c, core.WrapError(err, core.ErrFileNotFound, message))
	case errors.Is(err, fs.ErrPermission):
		core.HandleError(c, core.WrapError(err, core.ErrPermissionDenied, message))
	case errors.Is(err, filemanager.ErrQuotaExceeded),
		errors.Is(err, filemanager.ErrInsufficientSpace):
		core.HandleError(c, core.WrapError(err, core.ErrInsufficientStorage, message))
	case errors.Is(err, filemanager.ErrRootOperation):
		core.HandleError(c, core.WrapError(err, core.ErrForbidden, message))
	case errors.Is(err, filemanager.ErrInvalidPath),
		errors.Is(err, filemanager.ErrInvalidName),
		errors.Is(err, filemanager.ErrNotRegular),
		errors.Is(err, filemanager.ErrReservedPath),
		errors.Is(err, filemanager.ErrUnsafeRemoteURL),
		errors.Is(err, filemanager.ErrDownloadLimit),
		errors.Is(err, fs.ErrExist):
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, message))
	default:
		core.HandleError(c, core.NewError(core.ErrInternalError, message))
	}
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
