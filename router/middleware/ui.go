package middleware

import (
	"io/fs"
	"net/http"
	"oneinstack/utils/httpex"
	"oneinstack/webui"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// MidUiHandle serves the embedded SPA only for non-API routes that were not
// matched by the router.
func MidUiHandle(c *gin.Context) {
	requestPath := c.Request.URL.Path
	if requestPath == "/v1" || strings.HasPrefix(requestPath, "/v1/") {
		c.JSON(http.StatusNotFound, gin.H{
			"code":    "NOT_FOUND",
			"message": "API route not found",
		})
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}

	if requestPath == "/" {
		requestPath = "/index.html"
	}
	filePath := path.Clean(strings.TrimPrefix(requestPath, "/"))
	if !fs.ValidPath(filePath) {
		httpex.ResMsgUrl(c, "路径错误", "/")
		return
	}

	servedPath := filePath
	data, err := webui.ReadFile(filePath)
	if err != nil {
		// Extension-less paths are SPA routes. Missing assets stay 404 so a
		// broken deployment cannot be hidden behind index.html.
		if path.Ext(filePath) != "" {
			c.Status(http.StatusNotFound)
			return
		}
		servedPath = "index.html"
		data, err = webui.ReadFile(servedPath)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"code":    "WEB_UI_UNAVAILABLE",
				"message": "Web UI is unavailable",
			})
			return
		}
	}

	setCacheHeaders(c, servedPath)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "same-origin")
	c.Data(http.StatusOK, getContentType(servedPath), data)
}

func setCacheHeaders(c *gin.Context, filePath string) {
	if path.Ext(filePath) == ".html" {
		c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		return
	}
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
}

func getContentType(filePath string) string {
	switch strings.ToLower(path.Ext(filePath)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js":
		return "application/javascript; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
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
	default:
		return "application/octet-stream"
	}
}
