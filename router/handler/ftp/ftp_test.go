package ftp

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"oneinstack/app"
	"oneinstack/internal/services/filemanager"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFileHandlersUseVirtualPaths(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	if err := os.WriteFile(filepath.Join(rootPath, "index.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}

	listResponse := performJSONRequest(t, ListDirectory, `{"path":"/"}`)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}
	if !strings.Contains(listResponse.Body.String(), `"path":"/index.txt"`) {
		t.Fatalf("list does not contain virtual file path: %s", listResponse.Body.String())
	}
	if strings.Contains(listResponse.Body.String(), rootPath) {
		t.Fatalf("list leaked physical root path: %s", listResponse.Body.String())
	}

	contentResponse := performJSONRequest(t, Content, `{"path":"/index.txt"}`)
	if contentResponse.Code != http.StatusOK {
		t.Fatalf("content status = %d, body = %s", contentResponse.Code, contentResponse.Body.String())
	}
	if !strings.Contains(contentResponse.Body.String(), `"content":"hello"`) {
		t.Fatalf("unexpected content response: %s", contentResponse.Body.String())
	}
	if strings.Contains(contentResponse.Body.String(), rootPath) {
		t.Fatalf("content leaked physical root path: %s", contentResponse.Body.String())
	}

	saveResponse := performJSONRequest(t, SaveFile, `{"path":"/index.txt","content":""}`)
	if saveResponse.Code != http.StatusOK {
		t.Fatalf("empty save status = %d, body = %s", saveResponse.Code, saveResponse.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(rootPath, "index.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 0 {
		t.Fatalf("empty save left content %q", content)
	}
}

func TestFileHandlersRejectParentTraversal(t *testing.T) {
	configureTestFileRoot(t)

	tests := []struct {
		name    string
		handler gin.HandlerFunc
		body    string
	}{
		{name: "list", handler: ListDirectory, body: `{"path":"../"}`},
		{name: "create", handler: CreateFileOrDir, body: `{"path":"/sites/../../escape","type":"file"}`},
		{name: "download", handler: DownloadFile, body: `{"path":"../outside.txt"}`},
		{name: "delete", handler: DeleteFileOrDir, body: `{"path":"/sites/../outside.txt"}`},
		{name: "content", handler: Content, body: `{"path":"../outside.txt"}`},
		{name: "tree", handler: GetDirectoryTreeHandler, body: `{"path":"../"}`},
		{name: "save", handler: SaveFile, body: `{"path":"../outside.txt","content":"changed"}`},
		{name: "url download target", handler: UrlDownloadFile, body: `{"path":"../","url":"https://example.com/file","name":"file.txt"}`},
		{name: "reserved trash path", handler: ListDirectory, body: `{"path":"/.oneinstack-trash"}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := performJSONRequest(t, test.handler, test.body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestDeleteMovesFileToTrashAndRestoreAPI(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	originalPath := filepath.Join(rootPath, "recover.txt")
	if err := os.WriteFile(originalPath, []byte("recover"), 0600); err != nil {
		t.Fatal(err)
	}

	deleteResponse := performJSONRequest(t, DeleteFileOrDir, `{"path":"/recover.txt"}`)
	if deleteResponse.Code != http.StatusOK {
		t.Fatalf("delete status = %d, body = %s", deleteResponse.Code, deleteResponse.Body.String())
	}
	var payload struct {
		Data filemanager.TrashEntry `json:"data"`
	}
	if err := json.Unmarshal(deleteResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode delete response: %v", err)
	}
	if payload.Data.ID == "" || payload.Data.OriginalPath != "/recover.txt" {
		t.Fatalf("unexpected trash entry: %+v", payload.Data)
	}
	if _, err := os.Stat(originalPath); !os.IsNotExist(err) {
		t.Fatalf("deleted file still exists: %v", err)
	}

	listResponse := performJSONRequest(t, ListTrash, "")
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), payload.Data.ID) {
		t.Fatalf("trash list status = %d, body = %s", listResponse.Code, listResponse.Body.String())
	}

	restoreResponse := performJSONRequest(t, RestoreTrash, `{"id":"`+payload.Data.ID+`"}`)
	if restoreResponse.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", restoreResponse.Code, restoreResponse.Body.String())
	}
	content, err := os.ReadFile(originalPath)
	if err != nil || string(content) != "recover" {
		t.Fatalf("restored content=%q error=%v", content, err)
	}

	rootListResponse := performJSONRequest(t, ListDirectory, `{"path":"/"}`)
	if strings.Contains(rootListResponse.Body.String(), ".oneinstack-trash") {
		t.Fatalf("file list exposed internal trash directory: %s", rootListResponse.Body.String())
	}
}

func TestTrashRestoreConflictAndPermanentDeleteAPI(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	originalPath := filepath.Join(rootPath, "conflict.txt")
	if err := os.WriteFile(originalPath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	deleteResponse := performJSONRequest(t, DeleteFileOrDir, `{"path":"/conflict.txt"}`)
	var payload struct {
		Data filemanager.TrashEntry `json:"data"`
	}
	if err := json.Unmarshal(deleteResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}

	restoreResponse := performJSONRequest(t, RestoreTrash, `{"id":"`+payload.Data.ID+`"}`)
	if restoreResponse.Code != http.StatusBadRequest {
		t.Fatalf("conflict restore status = %d, body = %s", restoreResponse.Code, restoreResponse.Body.String())
	}
	permanentResponse := performJSONRequest(t, DeleteTrashPermanently, `{"id":"`+payload.Data.ID+`"}`)
	if permanentResponse.Code != http.StatusOK {
		t.Fatalf("permanent delete status = %d, body = %s", permanentResponse.Code, permanentResponse.Body.String())
	}
	content, err := os.ReadFile(originalPath)
	if err != nil || string(content) != "new" {
		t.Fatalf("permanent trash delete changed live file: content=%q error=%v", content, err)
	}
}

func TestEmptyTrashRequiresExplicitConfirmation(t *testing.T) {
	configureTestFileRoot(t)
	response := performJSONRequest(t, EmptyTrash, `{"confirm":false}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
}

func TestCapacityAPIAndSaveQuota(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	previousSystem := app.ONE_CONFIG.System
	t.Cleanup(func() {
		app.ONE_CONFIG.System = previousSystem
	})
	app.ONE_CONFIG.System.DefaultPath = rootPath
	app.ONE_CONFIG.System.FileUploadMaxBytes = 100
	app.ONE_CONFIG.System.FileEditMaxBytes = 100
	app.ONE_CONFIG.System.FileRootQuotaBytes = 5
	app.ONE_CONFIG.System.FileMinFreeBytes = 0
	app.ONE_CONFIG.System.TrashRetentionDays = 30
	app.ONE_CONFIG.System.TrashCleanupSchedule = "0 3 * * *"
	if err := os.WriteFile(filepath.Join(rootPath, "quota.txt"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}

	capacityResponse := performJSONRequest(t, Capacity, "")
	if capacityResponse.Code != http.StatusOK {
		t.Fatalf("capacity status = %d, body = %s", capacityResponse.Code, capacityResponse.Body.String())
	}
	if !strings.Contains(capacityResponse.Body.String(), `"writableBytes":0`) {
		t.Fatalf("unexpected capacity response: %s", capacityResponse.Body.String())
	}

	saveResponse := performJSONRequest(t, SaveFile, `{"path":"/quota.txt","content":"123456"}`)
	if saveResponse.Code != http.StatusInsufficientStorage {
		t.Fatalf("save status = %d, want 507; body = %s", saveResponse.Code, saveResponse.Body.String())
	}
	content, err := os.ReadFile(filepath.Join(rootPath, "quota.txt"))
	if err != nil || string(content) != "12345" {
		t.Fatalf("quota failure modified file: content=%q error=%v", content, err)
	}
}

func TestDeleteRejectsAuthorizedRoot(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	markerPath := filepath.Join(rootPath, "keep.txt")
	if err := os.WriteFile(markerPath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}

	response := performJSONRequest(t, DeleteFileOrDir, `{"path":"/"}`)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("authorized root contents were deleted: %v", err)
	}
}

func TestSaveCannotFollowSymlinkOutsideRoot(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "escape")); err != nil {
		t.Skipf("symlink is not supported: %v", err)
	}

	response := performJSONRequest(t, SaveFile, `{"path":"/escape","content":"changed"}`)
	if response.Code == http.StatusOK {
		t.Fatalf("save unexpectedly followed an external symlink: %s", response.Body.String())
	}
	content, err := os.ReadFile(outsidePath)
	if err != nil || string(content) != "secret" {
		t.Fatalf("outside target was modified: content=%q error=%v", content, err)
	}
}

func TestUploadRejectsTraversalTarget(t *testing.T) {
	configureTestFileRoot(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("path", "../"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("file", "payload.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/ftp/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request
	UploadFile(context)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
}

func TestURLDownloadRejectsPrivateTargets(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	response := performJSONRequest(t, UrlDownloadFile, `{
		"path":"/",
		"url":"http://169.254.169.254/latest/meta-data",
		"name":"metadata.txt"
	}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rootPath, "metadata.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked remote download created a file: %v", err)
	}
}

func configureTestFileRoot(t *testing.T) string {
	t.Helper()
	rootPath := t.TempDir()
	previous := app.ONE_CONFIG.System.DefaultPath
	app.ONE_CONFIG.System.DefaultPath = rootPath
	t.Cleanup(func() {
		app.ONE_CONFIG.System.DefaultPath = previous
	})
	return rootPath
}

func performJSONRequest(t *testing.T, handler gin.HandlerFunc, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	request := httptest.NewRequest(http.MethodPost, "/ftp/test", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request
	handler(context)
	return response
}
