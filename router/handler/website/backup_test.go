package website

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestWebsiteDeleteRequiresExactConfirmationInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/website/del", Delete)
	request := httptest.NewRequest(
		http.MethodPost,
		"/website/del",
		strings.NewReader(`{"id":1,"deleteFiles":true}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "确认") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
