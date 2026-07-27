package middleware

import (
	"net/http"
	"net/http/httptest"
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
