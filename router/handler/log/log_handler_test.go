package log

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"oneinstack/internal/models"
	logservice "oneinstack/internal/services/log"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func runtimeHandlerManager(t *testing.T) *logservice.Manager {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.RuntimeLogEntry{}); err != nil {
		t.Fatal(err)
	}
	manager, err := logservice.NewRuntimeManager(database, 30, "10 5 * * *")
	if err != nil {
		t.Fatal(err)
	}
	logservice.ConfigureRuntimeDefault(manager)
	t.Cleanup(func() { logservice.ClearRuntimeDefault(manager) })
	return manager
}

func TestRuntimeLogStreamResumesFromLastEventID(t *testing.T) {
	manager := runtimeHandlerManager(t)
	first, err := manager.Append(t.Context(), logservice.EntryInput{
		Level: logservice.LevelInfo, Source: "panel", Message: "first entry",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Append(t.Context(), logservice.EntryInput{
		Level: logservice.LevelError, Source: "panel", Message: "second entry",
	})
	if err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/stream", StreamRuntimeLogs)
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(requestContext)
	request.Header.Set("Last-Event-ID", strconv.FormatUint(first.ID, 10))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream response: status=%d headers=%v body=%s",
			response.Code, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "first entry") ||
		!strings.Contains(body, "second entry") ||
		!strings.Contains(body, "id: "+strconv.FormatUint(second.ID, 10)) ||
		strings.Contains(body, "id: "+strconv.FormatUint(first.ID, 10)+"\n") {
		t.Fatalf("unexpected resumed stream: %s", body)
	}
}

func TestRuntimeLogStreamRejectsOlderCursor(t *testing.T) {
	runtimeHandlerManager(t)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/stream", StreamRuntimeLogs)
	request := httptest.NewRequest(http.MethodGet, "/stream?beforeId=2", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
