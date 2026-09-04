package software

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/softwaretask"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
)

func TestMySQLInstallationUsesServerSideDefaultsAndDoesNotPersistSecret(t *testing.T) {
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x37}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := app.InitDB(filepath.Join(t.TempDir(), "mysql-defaults.db")); err != nil {
		t.Fatal(err)
	}
	requests := make(chan softwaretask.InstallRequest, 1)
	manager := softwaretask.NewManager(
		app.DB(),
		t.TempDir(),
		func(
			_ context.Context,
			request softwaretask.InstallRequest,
			logPath string,
			_ *softwaretask.Reporter,
		) error {
			requests <- request
			return os.WriteFile(logPath, []byte("ok\n"), 0600)
		},
	)
	previousManager := taskManager
	previousDB := taskManagerDB
	taskManager = manager
	taskManagerDB = app.DB()
	t.Cleanup(func() {
		taskManager = previousManager
		taskManagerDB = previousDB
	})

	task, err := SubmitInstallationTask(input.InstallParams{
		Key: "db", Version: "8.0.45",
	}, 7)
	if err != nil {
		t.Fatal(err)
	}
	var request softwaretask.InstallRequest
	select {
	case request = <-requests:
	case <-time.After(3 * time.Second):
		t.Fatal("MySQL installation request was not executed")
	}
	if request.Port != "3306" ||
		request.Username != "root" ||
		len(request.Password) != 24 {
		t.Fatalf("unexpected MySQL defaults: %#v", request)
	}
	reloaded, err := manager.Get(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reloaded.ParametersJSON, request.Password) ||
		strings.Contains(reloaded.ParametersJSON, "password") {
		t.Fatalf("task metadata persisted the generated password: %s", reloaded.ParametersJSON)
	}
}

func TestInstallationHandlerCreatesTaskAndStreamsTerminalEvent(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "handler.db")); err != nil {
		t.Fatal(err)
	}
	manager := softwaretask.NewManager(
		app.DB(),
		t.TempDir(),
		func(
			ctx context.Context,
			request softwaretask.InstallRequest,
			logPath string,
			reporter *softwaretask.Reporter,
		) error {
			if err := os.WriteFile(logPath, []byte("handler install log\n"), 0600); err != nil {
				return err
			}
			if request.Operation == "uninstall" {
				reporter.OnActionStart("uninstall")
				reporter.OnActionProgress("uninstall", 50, "remove_binary", "正在移除程序文件")
				reporter.OnActionComplete("uninstall")
				return nil
			}
			if softwaretaskServiceOperation(request.Operation) {
				reporter.OnActionStart(request.Operation)
				reporter.OnActionProgress(
					request.Operation,
					50,
					"service_"+request.Operation,
					"正在执行服务动作",
				)
				reporter.OnActionComplete(request.Operation)
				return nil
			}
			reporter.OnActionStart("precheck")
			reporter.OnActionComplete("precheck")
			reporter.OnActionStart("install")
			reporter.OnActionProgress("install", 50, "compile", "正在编译")
			reporter.OnActionComplete("install")
			reporter.OnActionStart("verify")
			reporter.OnActionComplete("verify")
			return nil
		},
	)
	previousManager := taskManager
	previousDB := taskManagerDB
	taskManager = manager
	taskManagerDB = app.DB()
	t.Cleanup(func() {
		taskManager = previousManager
		taskManagerDB = previousDB
	})

	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/v1/soft/install",
		bytes.NewBufferString(`{"key":"webserver","version":"1.28.2"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request
	context.Set(middleware.ContextUserID, int64(7))

	RunInstallation(context)
	if response.Code != http.StatusAccepted {
		t.Fatalf("install status = %d; response=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			TaskID string `json:"taskId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.TaskID == "" {
		t.Fatalf("install response did not include task ID: %s", response.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		task, err := manager.Get(body.Data.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == models.SoftwareTaskStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("task did not finish: %#v", task)
		}
		time.Sleep(10 * time.Millisecond)
	}

	streamRequest := httptest.NewRequest(
		http.MethodGet,
		"/v1/soft/tasks/"+body.Data.TaskID+"/events",
		nil,
	)
	streamResponse := httptest.NewRecorder()
	streamContext, _ := gin.CreateTestContext(streamResponse)
	streamContext.Request = streamRequest
	streamContext.Params = gin.Params{{Key: "id", Value: body.Data.TaskID}}
	streamContext.Set(middleware.ContextUserID, int64(7))

	StreamSoftwareTaskEvents(streamContext)
	if streamResponse.Code != http.StatusOK {
		t.Fatalf("stream status = %d", streamResponse.Code)
	}
	streamBody := streamResponse.Body.String()
	if !strings.Contains(streamBody, "event: progress") ||
		!strings.Contains(streamBody, "event: terminal") ||
		!strings.Contains(streamBody, `"status":"succeeded"`) {
		t.Fatalf("unexpected event stream: %s", streamBody)
	}

	downloadRequest := httptest.NewRequest(
		http.MethodGet,
		"/v1/soft/tasks/"+body.Data.TaskID+"/log/download",
		nil,
	)
	downloadResponse := httptest.NewRecorder()
	downloadContext, _ := gin.CreateTestContext(downloadResponse)
	downloadContext.Request = downloadRequest
	downloadContext.Params = gin.Params{{Key: "id", Value: body.Data.TaskID}}
	downloadContext.Set(middleware.ContextUserID, int64(7))
	DownloadSoftwareTaskLog(downloadContext)
	if downloadResponse.Code != http.StatusOK ||
		downloadResponse.Body.String() != "handler install log\n" ||
		!strings.Contains(downloadResponse.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf(
			"unexpected log download: status=%d headers=%v body=%q",
			downloadResponse.Code,
			downloadResponse.Header(),
			downloadResponse.Body.String(),
		)
	}

	forbiddenRequest := httptest.NewRequest(
		http.MethodGet,
		"/v1/soft/tasks/"+body.Data.TaskID+"/log/download",
		nil,
	)
	forbiddenResponse := httptest.NewRecorder()
	forbiddenContext, _ := gin.CreateTestContext(forbiddenResponse)
	forbiddenContext.Request = forbiddenRequest
	forbiddenContext.Params = gin.Params{{Key: "id", Value: body.Data.TaskID}}
	forbiddenContext.Set(middleware.ContextUserID, int64(8))
	DownloadSoftwareTaskLog(forbiddenContext)
	if forbiddenResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-user log download status = %d, want 403", forbiddenResponse.Code)
	}

	statsRequest := httptest.NewRequest(http.MethodGet, "/v1/soft/tasks/stats?days=30", nil)
	statsResponse := httptest.NewRecorder()
	statsContext, _ := gin.CreateTestContext(statsResponse)
	statsContext.Request = statsRequest
	statsContext.Set(middleware.ContextUserID, int64(7))
	GetSoftwareTaskStats(statsContext)
	if statsResponse.Code != http.StatusOK ||
		!strings.Contains(statsResponse.Body.String(), `"succeeded":1`) {
		t.Fatalf("unexpected task stats: %s", statsResponse.Body.String())
	}

	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "redis", "7.4.8").
		Updates(map[string]any{
			"installed":       true,
			"install_version": "7.4.8",
			"status":          models.Soft_Status_Suc,
		}).Error; err != nil {
		t.Fatal(err)
	}
	serviceRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/soft/services/redis/actions",
		bytes.NewBufferString(`{"action":"restart"}`),
	)
	serviceRequest.Header.Set("Content-Type", "application/json")
	serviceResponse := httptest.NewRecorder()
	serviceContext, _ := gin.CreateTestContext(serviceResponse)
	serviceContext.Request = serviceRequest
	serviceContext.Params = gin.Params{{Key: "component", Value: "redis"}}
	serviceContext.Set(middleware.ContextUserID, int64(7))
	RunComponentServiceAction(serviceContext)
	if serviceResponse.Code != http.StatusAccepted {
		t.Fatalf("service action status = %d; response=%s",
			serviceResponse.Code, serviceResponse.Body.String())
	}
	var serviceBody struct {
		Data struct {
			TaskID    string `json:"taskId"`
			Operation string `json:"operation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(serviceResponse.Body.Bytes(), &serviceBody); err != nil {
		t.Fatal(err)
	}
	serviceTask := waitForHandlerTask(
		t,
		manager,
		serviceBody.Data.TaskID,
		models.SoftwareTaskStatusSucceeded,
	)
	if serviceBody.Data.Operation != "restart" ||
		serviceTask.Message != "服务重启成功" {
		t.Fatalf("unexpected service task: response=%s task=%#v",
			serviceResponse.Body.String(), serviceTask)
	}

	removeRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/soft/remove",
		bytes.NewBufferString(`{"name":"redis","version":"7.4.8"}`),
	)
	removeRequest.Header.Set("Content-Type", "application/json")
	removeResponse := httptest.NewRecorder()
	removeContext, _ := gin.CreateTestContext(removeResponse)
	removeContext.Request = removeRequest
	removeContext.Set(middleware.ContextUserID, int64(7))
	RemoveSoftware(removeContext)
	if removeResponse.Code != http.StatusAccepted {
		t.Fatalf("uninstall status = %d; response=%s", removeResponse.Code, removeResponse.Body.String())
	}
	var removeBody struct {
		Data struct {
			TaskID    string `json:"taskId"`
			Operation string `json:"operation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(removeResponse.Body.Bytes(), &removeBody); err != nil {
		t.Fatal(err)
	}
	if removeBody.Data.TaskID == "" || removeBody.Data.Operation != "uninstall" {
		t.Fatalf("unexpected uninstall response: %s", removeResponse.Body.String())
	}
	removeTask := waitForHandlerTask(
		t,
		manager,
		removeBody.Data.TaskID,
		models.SoftwareTaskStatusSucceeded,
	)
	if removeTask.Operation != "uninstall" || removeTask.Phase != models.SoftwareTaskStatusSucceeded {
		t.Fatalf("unexpected uninstall task: %#v", removeTask)
	}
}

func softwaretaskServiceOperation(operation string) bool {
	switch operation {
	case "start", "stop", "restart", "reload":
		return true
	default:
		return false
	}
}

func waitForHandlerTask(
	t *testing.T,
	manager *softwaretask.Manager,
	taskID string,
	status string,
) *models.SoftwareTask {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.Get(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Status == status {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, err := manager.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("task status = %s, want %s", task.Status, status)
	return nil
}
