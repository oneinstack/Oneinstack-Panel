package static

import (
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed dist/*
var distFS embed.FS

// StaticFS 返回嵌入的静态文件系统
func StaticFS() http.FileSystem {
	// 获取dist子目录的文件系统
	dist, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	return http.FS(dist)
}

// StaticHandler 处理静态文件请求
func StaticHandler(c *gin.Context) {
	path := c.Param("filepath")
	if path == "" {
		path = "index.html"
	}

	// 清理路径
	path = filepath.Clean(path)
	if strings.HasPrefix(path, "..") {
		c.Status(http.StatusForbidden)
		return
	}

	// 设置缓存头
	setCacheHeaders(c, path)

	// 尝试从嵌入的文件系统读取文件
	data, err := fs.ReadFile(distFS, "dist/"+path)
	if err != nil {
		// 如果文件不存在，返回index.html（SPA路由）
		data, err = fs.ReadFile(distFS, "dist/index.html")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
	}

	// 设置Content-Type
	setContentType(c, path)

	// 返回文件内容
	c.Data(http.StatusOK, getContentType(path), data)
}

// setCacheHeaders 设置缓存头
func setCacheHeaders(c *gin.Context, path string) {
	ext := filepath.Ext(path)

	// HTML文件不缓存
	if ext == ".html" {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
	} else {
		// 其他静态文件缓存1年
		c.Header("Cache-Control", "public, max-age=31536000, immutable")
	}
}

// setContentType 设置Content-Type
func setContentType(c *gin.Context, path string) {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".html":
		c.Header("Content-Type", "text/html; charset=utf-8")
	case ".css":
		c.Header("Content-Type", "text/css; charset=utf-8")
	case ".js":
		c.Header("Content-Type", "application/javascript; charset=utf-8")
	case ".json":
		c.Header("Content-Type", "application/json; charset=utf-8")
	case ".png":
		c.Header("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		c.Header("Content-Type", "image/jpeg")
	case ".gif":
		c.Header("Content-Type", "image/gif")
	case ".svg":
		c.Header("Content-Type", "image/svg+xml")
	case ".ico":
		c.Header("Content-Type", "image/x-icon")
	case ".woff":
		c.Header("Content-Type", "font/woff")
	case ".woff2":
		c.Header("Content-Type", "font/woff2")
	case ".ttf":
		c.Header("Content-Type", "font/ttf")
	case ".eot":
		c.Header("Content-Type", "application/vnd.ms-fontobject")
	case ".otf":
		c.Header("Content-Type", "font/otf")
	case ".map":
		c.Header("Content-Type", "application/json")
	default:
		c.Header("Content-Type", "application/octet-stream")
	}
}

// getContentType 获取Content-Type
func getContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))

	switch ext {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".eot":
		return "application/vnd.ms-fontobject"
	case ".otf":
		return "font/otf"
	case ".map":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// GetFile 获取嵌入的文件内容
func GetFile(path string) ([]byte, error) {
	file, err := distFS.Open("dist/" + path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}

	data := make([]byte, stat.Size())
	_, err = file.Read(data)
	return data, err
}

// FileExists 检查文件是否存在
func FileExists(path string) bool {
	_, err := distFS.Open("dist/" + path)
	return err == nil
}
