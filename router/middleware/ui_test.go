package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestEmbeddedUIRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
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
			contains:    `"code":"NOT_FOUND"`,
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
