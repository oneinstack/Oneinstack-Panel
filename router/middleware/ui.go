package middleware

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"oneinstack/app"
	systemservice "oneinstack/internal/services/system"
	"oneinstack/utils/httpex"
	"oneinstack/webui"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

const panelEntryHintStylesheetPath = "/__panel-entry_hint.css"

// MidUiHandle serves the embedded SPA only for non-API routes that were not
// matched by the router.
func MidUiHandle(c *gin.Context) {
	requestPath := c.Request.URL.Path
	if requestPath == "/v1" || strings.HasPrefix(requestPath, "/v1/") {
		writeAPIError(c, http.StatusNotFound, "NOT_FOUND", "API 路由不存在", "未找到与当前 HTTP 方法和路径匹配的接口路由："+c.Request.Method+" "+requestPath)
		return
	}
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusNotFound)
		return
	}
	if requestPath == panelEntryHintStylesheetPath {
		c.Header("Cache-Control", "public, max-age=300")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Data(http.StatusOK, "text/css; charset=utf-8", []byte(panelEntryHintStylesheet()))
		return
	}

	panelEntryEnabled, panelEntryPath := panelEntryState()
	if panelEntryEnabled {
		if requestPath == panelEntryPath {
			requestPath = panelEntryPath + "/"
		}
		if requestPath != panelEntryPath && !strings.HasPrefix(requestPath, panelEntryPath+"/") {
			if path.Ext(requestPath) != "" {
				c.Status(http.StatusNotFound)
				return
			}
			c.Header("Cache-Control", "no-cache, no-store, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
			c.Header("X-Content-Type-Options", "nosniff")
			c.Header("Referrer-Policy", "same-origin")
			c.Data(http.StatusOK, "text/html; charset=utf-8", panelEntryHintPage(systemservice.PanelEntryCLICommand()))
			return
		}
		requestPath = strings.TrimPrefix(requestPath, panelEntryPath)
		if requestPath == "" || requestPath == "/" {
			requestPath = "/index.html"
		}
	} else if requestPath == "/" {
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
			writeAPIError(c, http.StatusInternalServerError, "WEB_UI_UNAVAILABLE", "Web UI 不可用", "无法从嵌入资源中读取 index.html，前端资源可能未构建或部署不完整。")
			return
		}
	}

	setCacheHeaders(c, servedPath)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "same-origin")
	if panelEntryEnabled && servedPath == "index.html" {
		data = rewriteIndexBasePath(data, panelEntryPath)
	}
	c.Data(http.StatusOK, getContentType(servedPath), data)
}

func PanelEntryGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		panelEntryEnabled, panelEntryPath := panelEntryState()
		if !panelEntryEnabled {
			c.Next()
			return
		}
		if panelEntryRequestAllowed(c.Request, panelEntryPath) {
			c.Next()
			return
		}
		writeAPIError(c, http.StatusNotFound, "NOT_FOUND", "请求资源不存在", "当前请求未通过面板入口路径校验，或 Origin/Referer 与面板入口不匹配。")
	}
}

func panelEntryRequestAllowed(request *http.Request, panelEntryPath string) bool {
	for _, candidate := range []string{request.Header.Get("Origin"), request.Header.Get("Referer")} {
		if originMatchesPanelEntry(candidate, request, panelEntryPath) {
			return true
		}
	}
	return false
}

func originMatchesPanelEntry(candidate string, request *http.Request, panelEntryPath string) bool {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return false
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	requestHost := request.Host
	if !strings.EqualFold(parsed.Host, requestHost) {
		return false
	}
	if parsed.Path == panelEntryPath || strings.HasPrefix(parsed.Path, panelEntryPath+"/") {
		return true
	}
	return false
}

func panelEntryState() (bool, string) {
	if !app.ONE_CONFIG.System.PanelEntryEnabled {
		return false, ""
	}
	path := strings.TrimSpace(app.ONE_CONFIG.System.PanelEntryPath)
	if path == "" {
		return false, ""
	}
	path = "/" + strings.Trim(path, "/")
	return true, path
}

func rewriteIndexBasePath(data []byte, panelEntryPath string) []byte {
	prefix := panelEntryPath
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	replacements := strings.NewReplacer(
		`<base href="/" />`, `<base href="`+prefix+`" />`,
		`href="/`, `href="`+prefix,
		`src="/`, `src="`+prefix,
	)
	return []byte(replacements.Replace(string(data)))
}

func panelEntryHintPage(command string) []byte {
	return []byte(fmt.Sprintf(`<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>暂时无法访问</title>
    <link rel="stylesheet" href="%s" />
  </head>
  <body>
    <main class="card">
      <h1>暂时无法访问</h1>
      <p>当前环境已经开启了安全入口登录</p>
      <p>可在 SSH 终端输入以下命令来查看面板入口：</p>
      <code>%s</code>
    </main>
  </body>
</html>`, panelEntryHintStylesheetPath, command))
}

func panelEntryHintStylesheet() string {
	return `:root {
  color-scheme: light;
}
* {
  box-sizing: border-box;
}
body {
  margin: 0;
  min-height: 100vh;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
  background: #f5f7fb;
  color: #4b5563;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 32px;
}
.card {
  width: min(1280px, 100%);
  background: #fff;
  border-radius: 28px;
  box-shadow: 0 18px 60px rgba(15, 23, 42, 0.08);
  padding: 72px 48px;
  text-align: center;
}
h1 {
  margin: 0 0 44px;
  font-size: clamp(32px, 5vw, 64px);
  line-height: 1.1;
  color: #4b5563;
  font-weight: 700;
}
p {
  margin: 0 0 28px;
  font-size: clamp(20px, 2.6vw, 34px);
  line-height: 1.7;
  color: #6b7280;
}
code {
  display: inline-block;
  margin-top: 18px;
  padding: 16px 28px;
  border-radius: 16px;
  background: #f3f4f6;
  color: #ef4444;
  font-size: clamp(24px, 2.8vw, 42px);
  font-weight: 600;
}`
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
