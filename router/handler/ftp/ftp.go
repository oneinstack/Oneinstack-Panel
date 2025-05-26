package ftp

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"oneinstack/core"
	"oneinstack/utils"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

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
	Type       string `json:"type"`     // 可自定义分类
	Mode       string `json:"mode"`     // 文件权限
	MimeType   string `json:"mimeType"` // 内容类型
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
	Children  []*FileNode `json:"children"`
}

// 列出目录内容
func ListDirectory(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("参数错误"), nil)
		return
	}
	absPath := filepath.Join(filepath.Clean(input.Path))
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	files, err := os.ReadDir(absPath)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	var fileInfos []gin.H
	for _, file := range files {
		info, _ := file.Info()
		fileInfos = append(fileInfos, gin.H{
			"name":        file.Name(),
			"isDir":       file.IsDir(),
			"permissions": fmt.Sprintf("%#o", info.Mode().Perm()),
			"user":        utils.LookupUser(int(info.Sys().(*syscall.Stat_t).Uid)),
			"group":       utils.LookupGroup(int(info.Sys().(*syscall.Stat_t).Gid)),
			"modTime":     info.ModTime().Format("2006-01-02 15:04:05"),
			"size":        utils.FormatBytes(info.Size()),
		})
	}
	core.HandleSuccess(c, gin.H{"files": fileInfos})
}

// 创建文件或目录
func CreateFileOrDir(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
		Type string `json:"type" binding:"required"` // "file" 或 "dir"
	}

	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("参数错误"), nil)
		return
	}

	absPath := filepath.Join(filepath.Clean(input.Path))
	switch input.Type {
	case "file":
		f, err := os.Create(absPath)
		if err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
		defer f.Close()
	case "dir":
		if err := os.MkdirAll(absPath, 0755); err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
	default:
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("无效类型"), nil)
		return
	}

	core.HandleSuccess(c, "创建成功")

}

// 上传文件
func UploadFile(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	path := c.PostForm("path")
	if path == "" {
		path = "/"
	}

	absPath := filepath.Join(filepath.Clean(path), file.Filename)
	if err := c.SaveUploadedFile(file, absPath); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "上传成功")
}

// 下载文件
func DownloadFile(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	filePath := filepath.Join(filepath.Clean(input.Path))
	file, err := os.Open(filePath)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	defer file.Close()
	info, _ := file.Stat()
	c.Header("Content-Length", strconv.FormatInt(info.Size(), 10))
	c.Header("Content-Disposition", "attachment; filename*=utf-8''"+url.PathEscape(info.Name()))
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}

// 删除文件或目录
func DeleteFileOrDir(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("参数错误"), nil)
		return
	}
	absPath := filepath.Join(filepath.Clean(input.Path))
	if err := os.RemoveAll(absPath); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}
	core.HandleSuccess(c, "删除成功")
}

// 修改文件或目录权限、用户、用户组
func ModifyFileOrDirAttributes(c *gin.Context) {
	var input struct {
		Path      string `json:"path" binding:"required"`
		Perm      string `json:"perm" binding:"required"`
		User      string `json:"user" binding:"required"`
		Group     string `json:"group" binding:"required"`
		Recursive bool   `json:"recursive"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("参数错误"), nil)
		return
	}

	absPath := filepath.Join(filepath.Clean(input.Path))

	// Check if the path exists to prevent nil pointer dereference
	if _, err := os.Stat(absPath); os.IsNotExist(err) {
		core.HandleError(c, http.StatusInternalServerError, fmt.Errorf("path does not exist"), nil)
		return
	}

	// 修改权限和用户、用户组的函数
	modifyAttributes := func(path string) error {
		perm, err := strconv.ParseUint(input.Perm, 8, 32)
		if err != nil {
			return err
		}
		if err := os.Chmod(path, os.FileMode(perm)); err != nil {
			return err
		}

		uid, err := lookupUserID(input.User)
		if err != nil {
			return err
		}
		gid, err := lookupGroupID(input.Group)
		if err != nil {
			return err
		}
		if err := os.Chown(path, uid, gid); err != nil {
			return err
		}
		return nil
	}

	if err := modifyAttributes(absPath); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	if input.Recursive {
		err := filepath.Walk(absPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			return modifyAttributes(path)
		})
		if err != nil {
			core.HandleError(c, http.StatusInternalServerError, err, nil)
			return
		}
	}
	core.HandleSuccess(c, "修改成功")
}

func lookupUserID(username string) (int, error) {
	user, err := user.Lookup(username)
	if err != nil {
		return -1, err
	}
	uid, err := strconv.Atoi(user.Uid)
	if err != nil {
		return -1, err
	}
	return uid, nil
}

func lookupGroupID(groupname string) (int, error) {
	group, err := user.LookupGroup(groupname)
	if err != nil {
		return -1, err
	}
	gid, err := strconv.Atoi(group.Gid)
	if err != nil {
		return -1, err
	}
	return gid, nil
}

func SearchFile(c *gin.Context) {

}

func Content(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusBadRequest, fmt.Errorf("参数错误"), nil)
		return
	}

	fullPath := filepath.Clean(input.Path)

	// 检查是否存在
	stat, err := os.Stat(fullPath)
	if err != nil {
		core.HandleError(c, http.StatusNotFound, fmt.Errorf("文件不存在: %s", fullPath), nil)
		return
	}

	// 读取内容
	data, err := os.ReadFile(fullPath)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	// 获取 UID GID
	sys := stat.Sys().(*syscall.Stat_t)
	uid := fmt.Sprintf("%d", sys.Uid)
	gid := fmt.Sprintf("%d", sys.Gid)

	// 获取用户名组名
	userName := getUserName(sys.Uid)
	groupName := getGroupName(sys.Gid)

	// MIME 类型
	mimeType := mime.TypeByExtension(filepath.Ext(fullPath))
	if mimeType == "" {
		mimeType = "text/plain; charset=utf-8"
	}

	// 是否软链
	isSymlink := false
	linkPath := ""
	if info, err := os.Lstat(fullPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		isSymlink = true
		link, _ := os.Readlink(fullPath)
		linkPath = link
	}

	// 是否隐藏
	isHidden := strings.HasPrefix(stat.Name(), ".")

	// 返回结构体
	result := FileDetail{
		Path:       fullPath,
		Name:       stat.Name(),
		User:       userName,
		Group:      groupName,
		UID:        uid,
		GID:        gid,
		Extension:  filepath.Ext(stat.Name()),
		Content:    string(data),
		Size:       stat.Size(),
		IsDir:      stat.IsDir(),
		IsSymlink:  isSymlink,
		IsHidden:   isHidden,
		LinkPath:   linkPath,
		Type:       "", // 你可以自定义类型分类逻辑
		Mode:       fmt.Sprintf("%#o", stat.Mode().Perm()),
		MimeType:   mimeType,
		ModTime:    stat.ModTime().Format(time.RFC3339Nano),
		Items:      nil,
		ItemTotal:  0,
		FavoriteID: 0,
		IsDetail:   false,
	}

	core.HandleSuccess(c, result)

}

func GetDirectoryTreeHandler(c *gin.Context) {
	var req utils.DirTreeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "参数错误: " + err.Error()})
		return
	}

	tree, err := utils.ScanDirectoryTree(req)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	core.HandleSuccess(c, tree)
}

func getUserName(uid uint32) string {
	u, err := user.LookupId(fmt.Sprintf("%d", uid))
	if err != nil {
		return "unknown"
	}
	return u.Username
}

func getGroupName(gid uint32) string {
	g, err := user.LookupGroupId(fmt.Sprintf("%d", gid))
	if err != nil {
		return "unknown"
	}
	return g.Name
}

func SaveFile(c *gin.Context) {
	var input struct {
		Path    string `json:"path" binding:"required"`
		Content string `json:"content" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusBadRequest, fmt.Errorf("参数错误"), nil)
		return
	}

	fullPath := filepath.Clean(input.Path)
	if err := os.WriteFile(fullPath, []byte(input.Content), 0644); err != nil {
		core.HandleError(c, http.StatusInternalServerError, err, nil)
		return
	}

	core.HandleSuccess(c, "保存成功")
}

func UrlDownloadFile(c *gin.Context) {
	var input struct {
		Path string `json:"path" binding:"required"`
		Url  string `json:"url" binding:"required"`
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, http.StatusBadRequest, fmt.Errorf("参数错误"), nil)
	}
	core.HandleSuccess(c, DownloadUrlFile(input.Url, input.Path, input.Name))
}

func DownloadUrlFile(url string, path string, name string) bool {
	resp, err := http.Get(url)
	if err != nil {
		fmt.Println(err)
		return false
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath.Join(path, name))
	if err != nil {
		fmt.Println(err)
		return false
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		fmt.Println(err)
		return false
	}
	return true
}
