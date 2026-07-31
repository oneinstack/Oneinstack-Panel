package middleware

import (
	"net/http"
	"net/http/httptest"
	"oneinstack/app"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestSecurityHeadersKeepHTTPAvailableWithoutHSTS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://panel.test/", nil))

	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing content type protection")
	}
	if recorder.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("HTTP response must not opt the IP-based HTTP entry point into HSTS")
	}
}

func TestSecurityHeadersDoNotMakeOptionalHTTPSHostWide(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "http://panel.test/", nil)
	request.RemoteAddr = "192.0.2.10:443"
	request.Header.Set("X-Forwarded-Proto", "https")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Header().Get("Strict-Transport-Security") != "" {
		t.Fatal("optional HTTPS must not make the supported HTTP entry point unavailable through HSTS")
	}
}

func TestSecurityHeadersDefaultToSecureCSP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://panel.test/", nil))

	csp := recorder.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Fatal("missing content security policy")
	}
	if containsToken(csp, "ws:") {
		t.Fatalf("CSP must not allow insecure websocket connections by default: %q", csp)
	}
	if containsToken(csp, "'unsafe-inline'") {
		t.Fatalf("CSP must not allow inline styles by default: %q", csp)
	}
	if !containsToken(csp, "wss:") {
		t.Fatalf("CSP must allow secure websocket connections: %q", csp)
	}
}

func TestSecurityHeadersAllowExplicitCompatibilityFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previous := app.ONE_CONFIG.System
	app.ONE_CONFIG.System.AllowInsecureWebSocketInDev = true
	app.ONE_CONFIG.System.AllowInlineStyle = true
	t.Cleanup(func() {
		app.ONE_CONFIG.System = previous
	})

	router := gin.New()
	router.Use(SecurityHeaders())
	router.GET("/", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://panel.test/", nil))

	csp := recorder.Header().Get("Content-Security-Policy")
	if !containsToken(csp, "ws:") {
		t.Fatalf("explicit dev websocket flag should add ws: to CSP: %q", csp)
	}
	if !containsToken(csp, "'unsafe-inline'") {
		t.Fatalf("explicit inline style flag should add unsafe-inline to CSP: %q", csp)
	}
}

func containsToken(value, token string) bool {
	for _, field := range strings.Fields(value) {
		if strings.TrimRight(field, ";") == token {
			return true
		}
	}
	return false
}
