package cron

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestCustomShellRequiresExplicitRiskConfirmation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/cron/add", AddCron)
	request := httptest.NewRequest(
		http.MethodPost,
		"/cron/add",
		strings.NewReader(`{
			"name":"unsafe","task_type":"shell","command":"id",
			"schedule":["0 0 * * *"],"timeout_seconds":30,
			"concurrency_policy":"forbid"
		}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), "显式确认风险") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestCronTemplatesEndpointDoesNotExposeCommands(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/cron/templates", ListTemplates)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/cron/templates", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK ||
		!strings.Contains(body, "disk-usage-report") ||
		!strings.Contains(body, "service-status") ||
		strings.Contains(body, `"/bin/`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
}

func TestCSVSafePreventsSpreadsheetFormulaInjection(t *testing.T) {
	if value := csvSafe("  =HYPERLINK(\"https://example.com\")"); !strings.HasPrefix(value, "'") {
		t.Fatalf("formula was not escaped: %q", value)
	}
	if value := csvSafe("normal output"); value != "normal output" {
		t.Fatalf("normal output changed: %q", value)
	}
}
