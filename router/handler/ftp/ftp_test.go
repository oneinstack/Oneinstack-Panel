package ftp

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"oneinstack/app"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/internal/services/filemanager"
	"oneinstack/router/middleware"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
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

func TestSaveFileRejectsStaleRevisionAndBinaryEditorInput(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	target := filepath.Join(rootPath, "config.conf")
	if err := os.WriteFile(target, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}

	contentResponse := performJSONRequest(t, Content, `{"path":"/config.conf"}`)
	if contentResponse.Code != http.StatusOK {
		t.Fatalf("content status = %d, body = %s", contentResponse.Code, contentResponse.Body.String())
	}
	var contentPayload struct {
		Data FileDetail `json:"data"`
	}
	if err := json.Unmarshal(contentResponse.Body.Bytes(), &contentPayload); err != nil {
		t.Fatal(err)
	}
	if contentPayload.Data.Revision == "" {
		t.Fatal("content response did not include a revision")
	}
	if err := os.WriteFile(target, []byte("changed elsewhere"), 0600); err != nil {
		t.Fatal(err)
	}
	saveBody, err := json.Marshal(gin.H{
		"path": "/config.conf", "content": "panel change", "revision": contentPayload.Data.Revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	saveResponse := performJSONRequest(t, SaveFile, string(saveBody))
	if saveResponse.Code != http.StatusConflict {
		t.Fatalf("stale save status = %d, body = %s", saveResponse.Code, saveResponse.Body.String())
	}
	current, err := os.ReadFile(target)
	if err != nil || string(current) != "changed elsewhere" {
		t.Fatalf("stale save changed file: content=%q error=%v", current, err)
	}

	if err := os.WriteFile(filepath.Join(rootPath, "binary.bin"), []byte{0, 1, 2}, 0600); err != nil {
		t.Fatal(err)
	}
	binaryResponse := performJSONRequest(t, Content, `{"path":"/binary.bin"}`)
	if binaryResponse.Code != http.StatusBadRequest {
		t.Fatalf("binary content status = %d, body = %s", binaryResponse.Code, binaryResponse.Body.String())
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

func TestSearchFileReturnsBoundedMatches(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	if err := os.MkdirAll(filepath.Join(rootPath, "sites", "demo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "sites", "demo", "nginx.conf"), []byte("server {}"), 0644); err != nil {
		t.Fatal(err)
	}

	response := performJSONRequest(t, SearchFile, `{
		"path":"/",
		"query":"NGINX",
		"type":"file",
		"maxResults":20
	}`)
	if response.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"path":"/sites/demo/nginx.conf"`) {
		t.Fatalf("search result missing expected path: %s", response.Body.String())
	}
}

func TestFileMutationCreatesDetailedOperationRecord(t *testing.T) {
	configureTestFileRoot(t)
	database, err := gorm.Open(sqlite.Open("file:ftp-operation-audit?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.AuditEvent{}, &models.AuditCheckpoint{}, &models.AuditChainState{}); err != nil {
		t.Fatal(err)
	}
	key := sha256.Sum256([]byte("ftp-operation-audit-key"))
	manager, err := auditservice.ConfigureDefault(database, key[:])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auditservice.ClearDefault(manager) })

	response := performJSONRequest(t, CreateFileOrDir, `{"path":"/created.txt","type":"file"}`)
	if response.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
	}
	result, err := manager.List(auditservice.Filter{EventType: "file"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.Items[0].Action != "file.create" ||
		result.Items[0].Path != "/created.txt" || result.Items[0].Outcome != "success" {
		t.Fatalf("unexpected operation record: %+v", result.Items)
	}
}

func TestFileActionHandlersCopyMoveRenameArchiveAndProperties(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	if err := os.MkdirAll(filepath.Join(rootPath, "source", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "source", "nested", "app.conf"), []byte("server"), 0644); err != nil {
		t.Fatal(err)
	}

	copyResponse := performJSONRequest(t, CopyFileOrDir, `{
		"source":"/source",
		"targetDir":"/",
		"targetName":"copied"
	}`)
	if copyResponse.Code != http.StatusOK {
		t.Fatalf("copy status=%d body=%s", copyResponse.Code, copyResponse.Body.String())
	}
	renameResponse := performJSONRequest(t, RenameFileOrDir, `{
		"path":"/copied",
		"newName":"renamed"
	}`)
	if renameResponse.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renameResponse.Code, renameResponse.Body.String())
	}
	if err := os.Mkdir(filepath.Join(rootPath, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	moveResponse := performJSONRequest(t, MoveFileOrDir, `{
		"source":"/renamed",
		"targetDir":"/target"
	}`)
	if moveResponse.Code != http.StatusOK {
		t.Fatalf("move status=%d body=%s", moveResponse.Code, moveResponse.Body.String())
	}
	propertiesResponse := performJSONRequest(t, GetFileProperties, `{
		"path":"/target/renamed/nested/app.conf"
	}`)
	if propertiesResponse.Code != http.StatusOK ||
		!strings.Contains(propertiesResponse.Body.String(), `"permissions":"0644"`) ||
		!strings.Contains(propertiesResponse.Body.String(), `"owner":`) {
		t.Fatalf("properties status=%d body=%s", propertiesResponse.Code, propertiesResponse.Body.String())
	}
	archiveResponse := performJSONRequest(t, ArchiveFileOrDir, `{
		"path":"/target/renamed",
		"targetDir":"/",
		"archiveName":"renamed.tar.gz"
	}`)
	if archiveResponse.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archiveResponse.Code, archiveResponse.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rootPath, "renamed.tar.gz")); err != nil {
		t.Fatalf("archive was not created: %v", err)
	}
}

func TestPreviewImageAcceptsVerifiedRasterAndRejectsUnsafeContent(t *testing.T) {
	rootPath := configureTestFileRoot(t)
	pngContent := append(
		[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		make([]byte, 64)...,
	)
	if err := os.WriteFile(filepath.Join(rootPath, "preview.png"), pngContent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "active.svg"), []byte(`<svg onload="alert(1)"/>`), 0600); err != nil {
		t.Fatal(err)
	}
	largePath := filepath.Join(rootPath, "large.png")
	if err := os.WriteFile(largePath, pngContent, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(largePath, maxImagePreviewBytes+1); err != nil {
		t.Fatal(err)
	}

	issueTicket := func(path string) string {
		request := httptest.NewRequest(
			http.MethodPost,
			"/ftp/preview-ticket",
			strings.NewReader(`{"path":"`+path+`"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = request
		context.Set(middleware.ContextUserID, int64(1))
		context.Set(middleware.ContextSessionID, "preview-test-session")
		CreateImagePreviewTicket(context)
		if response.Code != http.StatusOK {
			t.Fatalf("issue preview ticket for %s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var payload struct {
			Data struct {
				URL string `json:"url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return payload.Data.URL
	}
	performPreview := func(path, sessionID string) *httptest.ResponseRecorder {
		ticketURL := issueTicket(path)
		token := ticketURL[strings.LastIndex(ticketURL, "/")+1:]
		request := httptest.NewRequest(http.MethodGet, ticketURL, nil)
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = request
		context.Params = gin.Params{{Key: "ticket", Value: token}}
		context.Set(middleware.ContextUserID, int64(1))
		context.Set(middleware.ContextSessionID, sessionID)
		PreviewImage(context)
		return response
	}
	valid := performPreview("/preview.png", "preview-test-session")
	if valid.Code != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", valid.Code, valid.Body.String())
	}
	if contentType := valid.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("preview content type=%q", contentType)
	}
	if disposition := valid.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, "inline;") {
		t.Fatalf("preview disposition=%q", disposition)
	}
	if valid.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("preview response is missing nosniff")
	}

	for _, path := range []string{"/active.svg", "/large.png"} {
		response := performPreview(path, "preview-test-session")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe preview %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	wrongSession := performPreview("/preview.png", "another-session")
	if wrongSession.Code != http.StatusNotFound {
		t.Fatalf("cross-session preview status=%d body=%s", wrongSession.Code, wrongSession.Body.String())
	}
}

func TestFileShareStoresOnlyTokenHashAndRejectsChangedFile(t *testing.T) {
	if err := app.InitDB("file:ftp-share-tests?mode=memory&cache=shared"); err != nil {
		t.Fatal(err)
	}
	rootPath := configureTestFileRoot(t)
	target := filepath.Join(rootPath, "release.zip")
	if err := os.WriteFile(target, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	createResponse := performJSONRequest(t, CreateFileShare, `{
		"path":"/release.zip",
		"expiryHours":2
	}`)
	if createResponse.Code != http.StatusOK {
		t.Fatalf("create share status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var payload struct {
		Data struct {
			Share       models.FileShare `json:"share"`
			DownloadURL string           `json:"downloadUrl"`
		} `json:"data"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(payload.Data.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	if token == "" {
		t.Fatalf("share response missing token: %s", createResponse.Body.String())
	}
	var stored models.FileShare
	if err := app.DB().First(&stored, "id = ?", payload.Data.Share.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TokenHash == token || stored.TokenHash != shareTokenHash(token) {
		t.Fatal("share token was not stored as a one-way hash")
	}

	download := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, payload.Data.DownloadURL, nil)
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = request
		DownloadSharedFile(context)
		return response
	}
	first := download()
	if first.Code != http.StatusOK || first.Body.String() != "release" {
		t.Fatalf("share download status=%d body=%q", first.Code, first.Body.String())
	}
	if err := os.WriteFile(target, []byte("changed release"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := download()
	if changed.Code != http.StatusNotFound {
		t.Fatalf("changed share status=%d body=%s", changed.Code, changed.Body.String())
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
