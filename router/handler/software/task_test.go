package software

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oneinstack/app"
	"oneinstack/config"
	"oneinstack/internal/models"
	"oneinstack/internal/services/scriptregistry"
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
	configureClosedLoopPackage(t, "db", "mysql", "8.0.45")
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
	configureClosedLoopPackage(t, "webserver", "nginx", "1.28.2")
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

// configureClosedLoopPackage gives handler tests the same package proof that
// production receives from Center: an exact catalog row, a signed package
// digest, and a package that can be downloaded and extracted. The tests keep
// the real validation path enabled instead of weakening it for test callers.
func configureClosedLoopPackage(t *testing.T, catalogKey, component, softwareVersion string) {
	t.Helper()
	originalConfig := app.ONE_CONFIG
	server, metadata := testPackageServer(t, component, softwareVersion)
	centerConfig := config.ScriptCenter{
		Enabled:               true,
		AllowInsecureHTTP:     true,
		URL:                   server.URL,
		Channel:               "stable",
		RequestTimeoutSeconds: 5,
		MaxPackageBytes:       8 << 20,
		MaxExpandedBytes:      32 << 20,
		CachePath:             t.TempDir(),
		BundledPath:           filepath.Join(t.TempDir(), "missing"),
		TrustedKeys:           metadata.trustedKeys,
	}
	app.ONE_CONFIG.ScriptCenter = centerConfig
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", catalogKey, softwareVersion).
		Updates(map[string]any{
			"component":              component,
			"catalog_managed":        true,
			"catalog_visible":        true,
			"installable":            true,
			"recommended":            true,
			"catalog_channel":        "stable",
			"latest_package_version": "1.0.0",
		}).Error; err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ? AND catalog_managed = ? AND latest_package_version = ?", catalogKey, softwareVersion, true, "1.0.0").
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("closed-loop catalog fixture was not created for %s %s", catalogKey, softwareVersion)
	}
	t.Cleanup(func() {
		server.Close()
		app.ONE_CONFIG = originalConfig
	})
}

type testPackageMetadata struct {
	scriptregistry.Metadata
	trustedKeys map[string]string
}

func testPackageServer(t *testing.T, component, softwareVersion string) (*httptest.Server, testPackageMetadata) {
	t.Helper()
	archive := testComponentPackageArchive(t, component, softwareVersion)
	digest := sha256.Sum256(archive)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(publicKey)
	keyID := hex.EncodeToString(keyDigest[:8])
	digestHex := hex.EncodeToString(digest[:])
	packageVersion := "1.0.0"
	metadata := scriptregistry.Metadata{
		Manifest: scriptregistry.Manifest{
			SchemaVersion: 1,
			Component: scriptregistry.Component{
				ID:               component,
				Name:             component,
				Version:          packageVersion,
				SoftwareVersions: []string{softwareVersion},
				Channel:          "stable",
			},
			Compatibility: scriptregistry.Compatibility{
				Systems:       []scriptregistry.System{{ID: "ubuntu", Versions: []string{"*"}}},
				Architectures: []string{"amd64"},
			},
			Actions: scriptregistry.Actions{
				Precheck:  "scripts/precheck.sh",
				Install:   "scripts/install.sh",
				Verify:    "scripts/verify.sh",
				Uninstall: "scripts/uninstall.sh",
			},
		},
		SHA256:    digestHex,
		Size:      int64(len(archive)),
		KeyID:     keyID,
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(fmt.Sprintf("oneinstack-script-package-v1\n%s\n%s\n%s\n%d\n", component, packageVersion, digestHex, len(archive))))),
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health/ready":
			response.WriteHeader(http.StatusOK)
		case "/v1/packages/resolve":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(metadata)
		case "/download":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	}))
	metadata.DownloadURL = server.URL + "/download"
	return server, testPackageMetadata{
		Metadata:    metadata,
		trustedKeys: map[string]string{keyID: base64.StdEncoding.EncodeToString(publicKey)},
	}
}

func testComponentPackageArchive(t *testing.T, component, softwareVersion string) []byte {
	t.Helper()
	manifest := []byte(fmt.Sprintf(`schemaVersion: 1
component:
  id: %s
  name: %s
  version: 1.0.0
  softwareVersions: ["%s"]
  channel: stable
compatibility:
  systems:
    - id: ubuntu
      versions: ["*"]
  architectures: [amd64]
actions:
  precheck: scripts/precheck.sh
  install: scripts/install.sh
  verify: scripts/verify.sh
  uninstall: scripts/uninstall.sh
`, component, component, softwareVersion))
	script := []byte("#!/usr/bin/env bash\nset -Eeuo pipefail\n")
	scriptDigest := sha256.Sum256(script)
	checksums := []byte(
		hex.EncodeToString(scriptDigest[:]) + "  scripts/precheck.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/install.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/verify.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/uninstall.sh\n",
	)
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	writeTarFile := func(name string, mode int64, contents []byte) {
		t.Helper()
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	writeTarFile("manifest.yaml", 0644, manifest)
	for _, name := range []string{"scripts/precheck.sh", "scripts/install.sh", "scripts/verify.sh", "scripts/uninstall.sh"} {
		writeTarFile(name, 0755, script)
	}
	writeTarFile("files.sha256", 0644, checksums)
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
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
