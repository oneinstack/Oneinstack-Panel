package middleware

import (
	"net/http"
	"net/http/httptest"
	"oneinstack/app"
	"oneinstack/webui"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEmbeddedUIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := app.ONE_CONFIG.System
	app.ONE_CONFIG.System.PanelEntryEnabled = false
	app.ONE_CONFIG.System.PanelEntryPath = ""
	t.Cleanup(func() {
		app.ONE_CONFIG.System = previous
	})
	router := gin.New()
	router.NoRoute(MidUiHandle)

	tests := []struct {
		name        string
		path        string
		status      int
		contentType string
		contains    string
	}{
		{
			name:        "root",
			path:        "/",
			status:      http.StatusOK,
			contentType: "text/html",
			contains:    "<!DOCTYPE html>",
		},
		{
			name:        "spa fallback",
			path:        "/settings/security",
			status:      http.StatusOK,
			contentType: "text/html",
			contains:    "<!DOCTYPE html>",
		},
		{
			name:   "missing asset",
			path:   "/static/missing.js",
			status: http.StatusNotFound,
		},
		{
			name:        "missing API",
			path:        "/v1/does-not-exist",
			status:      http.StatusNotFound,
			contentType: "application/json",
			contains:    `"code":1001`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s",
					recorder.Code, test.status, recorder.Body.String())
			}
			if test.contentType != "" &&
				!strings.Contains(recorder.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("content type = %q, want %q",
					recorder.Header().Get("Content-Type"), test.contentType)
			}
			if test.contains != "" && !strings.Contains(
				strings.ToLower(recorder.Body.String()),
				strings.ToLower(test.contains),
			) {
				t.Fatalf("body does not contain %q", test.contains)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/settings/security", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("non-GET SPA fallback status = %d, want 404", recorder.Code)
	}
}

func TestEmbeddedUIRoutesWithPanelEntry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := app.ONE_CONFIG.System
	app.ONE_CONFIG.System.PanelEntryEnabled = true
	app.ONE_CONFIG.System.PanelEntryPath = "/AbCd123456"
	t.Cleanup(func() {
		app.ONE_CONFIG.System = previous
	})
	router := gin.New()
	router.NoRoute(MidUiHandle)
	assetPath := mustEmbeddedUIAssetPath(t)

	tests := []struct {
		name     string
		path     string
		status   int
		contains string
	}{
		{name: "root hint", path: "/", status: http.StatusOK, contains: "当前环境已经开启了安全入口登录"},
		{name: "hint stylesheet", path: panelEntryHintStylesheetPath, status: http.StatusOK, contains: ".card {"},
		{name: "correct entry", path: "/AbCd123456", status: http.StatusOK, contains: `<base href="/AbCd123456/" />`},
		{name: "spa child route", path: "/AbCd123456/settings/security", status: http.StatusOK, contains: `/AbCd123456/static/page/`},
		{name: "asset under entry", path: "/AbCd123456" + assetPath, status: http.StatusOK},
		{name: "wrong entry hint", path: "/wrong-entry", status: http.StatusOK, contains: "one entrance"},
		{name: "wrong asset still missing", path: "/wrong-entry.js", status: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, test.status, recorder.Body.String())
			}
			if test.contains != "" && !strings.Contains(recorder.Body.String(), test.contains) {
				t.Fatalf("body does not contain %q", test.contains)
			}
		})
	}
}

func TestPanelEntryGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := app.ONE_CONFIG.System
	app.ONE_CONFIG.System.PanelEntryEnabled = true
	app.ONE_CONFIG.System.PanelEntryPath = "/AbCd123456"
	t.Cleanup(func() {
		app.ONE_CONFIG.System = previous
	})

	router := gin.New()
	router.POST("/v1/login", PanelEntryGuard(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	allowed := httptest.NewRequest(http.MethodPost, "/v1/login", nil)
	allowed.Host = "panel.example.com:8089"
	allowed.Header.Set("Origin", "http://panel.example.com:8089/AbCd123456")
	allowedRecorder := httptest.NewRecorder()
	router.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusNoContent {
		t.Fatalf("allowed request status = %d, want 204", allowedRecorder.Code)
	}

	blocked := httptest.NewRequest(http.MethodPost, "/v1/login", nil)
	blocked.Host = "panel.example.com:8089"
	blocked.Header.Set("Origin", "http://panel.example.com:8089/")
	blockedRecorder := httptest.NewRecorder()
	router.ServeHTTP(blockedRecorder, blocked)
	if blockedRecorder.Code != http.StatusNotFound {
		t.Fatalf("blocked request status = %d, want 404", blockedRecorder.Code)
	}
}

func mustEmbeddedUIAssetPath(t *testing.T) string {
	t.Helper()

	indexHTML, err := webui.ReadFile("index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}

	matches := regexp.MustCompile(`src="(/static/page/[^"]+\.js)"`).FindSubmatch(indexHTML)
	if len(matches) != 2 {
		t.Fatalf("embedded index.html does not contain a static page script path")
	}
	return string(matches[1])
}
