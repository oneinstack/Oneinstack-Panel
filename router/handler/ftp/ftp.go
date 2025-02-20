package ftp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"oneinstack/core"
	"oneinstack/utils"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/gin-gonic/gin"
)

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

	stat, err := file.Stat()
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "file not found"})
		return
	}

	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", url.QueryEscape(filepath.Base(filePath))))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Length", fmt.Sprintf("%d", stat.Size()))
	io.Copy(c.Writer, file)
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
