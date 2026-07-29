package softwarecatalog

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"oneinstack/config"
	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSyncAppliesSignedCenterCatalogAndPreservesOfflineSnapshot(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	document := signTestDocument(t, privateKey, []Product{{
		Key: "webserver", Component: "nginx", Name: "Nginx",
		Description: "Center managed Nginx", Tags: []string{"Web服务器"},
		Visible: true, Installable: true, Order: 10,
		Versions: []Version{
			{Version: "1.28.2", Channel: "stable", Enabled: true, Recommended: true},
			{Version: "1.29.0-beta", Channel: "stable", Enabled: false},
		},
	}})
	fail := false

	db := openCatalogTestDB(t)
	if err := db.Create(&models.Software{
		Key: "java", Component: "java", Name: "Java", Version: "11",
		CatalogVisible: true, Installable: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Software{
		Key: "webserver", Component: "nginx", Name: "Nginx", Version: "1.24.0",
		Installed: true, InstallVersion: "1.24.0", CatalogVisible: true, Installable: true,
	}).Error; err != nil {
		t.Fatal(err)
	}
	manager, err := New(config.ScriptCenter{
		Enabled: true, URL: "http://center.test", Channel: "stable",
		RequestTimeoutSeconds: 5, CatalogStaleAfterHours: 24,
		TrustedKeys: map[string]string{"test-key": base64.StdEncoding.EncodeToString(publicKey)},
	}, db)
	if err != nil {
		t.Fatal(err)
	}
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if fail {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("offline")),
				Header:     make(http.Header),
			}, nil
		}
		body, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
		}, nil
	})}
	status, err := manager.Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "center" || status.ProductCount != 1 || status.VersionCount != 2 ||
		status.Revision != document.Revision {
		t.Fatalf("unexpected status: %+v", status)
	}
	var published models.Software
	if err := db.Where("`key` = ? AND version = ?", "webserver", "1.28.2").First(&published).Error; err != nil {
		t.Fatal(err)
	}
	if !published.CatalogManaged || !published.CatalogVisible || !published.Installable ||
		!published.Recommended || published.Component != "nginx" {
		t.Fatalf("unexpected published row: %+v", published)
	}
	var disabled models.Software
	if err := db.Where("`key` = ? AND version = ?", "webserver", "1.29.0-beta").First(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if disabled.CatalogVisible || disabled.Installable || disabled.Recommended {
		t.Fatalf("disabled version remained available in the Panel store: %+v", disabled)
	}
	var removed models.Software
	if err := db.Where("`key` = ? AND version = ?", "java", "11").First(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if !removed.CatalogManaged || removed.CatalogVisible || removed.Installable {
		t.Fatalf("legacy row was not hidden by Center: %+v", removed)
	}
	var installed models.Software
	if err := db.Where("`key` = ? AND version = ?", "webserver", "1.24.0").First(&installed).Error; err != nil {
		t.Fatal(err)
	}
	if !installed.Installed || installed.CatalogVisible {
		t.Fatalf("installed legacy version was not preserved correctly: %+v", installed)
	}

	fail = true
	offlineStatus, err := manager.Sync(context.Background())
	if err == nil {
		t.Fatal("expected offline synchronization error")
	}
	if offlineStatus.Mode != "center-cache" || offlineStatus.Revision != document.Revision ||
		offlineStatus.LastError == "" {
		t.Fatalf("unexpected offline status: %+v", offlineStatus)
	}
	if err := db.Where("`key` = ? AND version = ?", "webserver", "1.28.2").First(&published).Error; err != nil {
		t.Fatal("verified snapshot was removed after Center failure:", err)
	}
}

func TestSyncRejectsTamperedCatalog(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	document := signTestDocument(t, privateKey, []Product{{
		Key: "redis", Component: "redis", Name: "Redis",
		Visible: true, Installable: true,
		Versions: []Version{{
			Version: "7.4.8", Channel: "stable", Enabled: true, Recommended: true,
		}},
	}})
	document.Products[0].Name = "Tampered"
	manager, err := New(config.ScriptCenter{
		Enabled: true, URL: "http://center.test", Channel: "stable",
		RequestTimeoutSeconds: 5, CatalogStaleAfterHours: 24,
		TrustedKeys: map[string]string{"test-key": base64.StdEncoding.EncodeToString(publicKey)},
	}, openCatalogTestDB(t))
	if err != nil {
		t.Fatal(err)
	}
	manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, marshalErr := json.Marshal(document)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(string(body))),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    request,
		}, nil
	})}
	status, err := manager.Sync(context.Background())
	if err == nil || status.Mode != "local-fallback" || status.Revision != "" {
		t.Fatalf("tampered catalog was not rejected: status=%+v err=%v", status, err)
	}
}

func TestSyncReappliesUnchangedCatalogWhenChannelChanges(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	document := signTestDocument(t, privateKey, []Product{{
		Key: "webserver", Component: "nginx", Name: "Nginx",
		Visible: true, Installable: true,
		Versions: []Version{
			{Version: "1.28.2", Channel: "stable", Enabled: true, Recommended: true},
			{Version: "1.31.0", Channel: "development", Enabled: true, Recommended: true},
		},
	}})
	db := openCatalogTestDB(t)
	publicKeyEncoded := base64.StdEncoding.EncodeToString(publicKey)
	newManager := func(channel string) *Manager {
		t.Helper()
		manager, managerErr := New(config.ScriptCenter{
			Enabled: true, URL: "http://center.test", Channel: channel,
			RequestTimeoutSeconds: 5, CatalogStaleAfterHours: 24,
			TrustedKeys: map[string]string{"test-key": publicKeyEncoded},
		}, db)
		if managerErr != nil {
			t.Fatal(managerErr)
		}
		manager.client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.Header.Get("If-None-Match") != "" {
				return &http.Response{
					StatusCode: http.StatusNotModified,
					Body:       io.NopCloser(strings.NewReader("")),
					Header:     make(http.Header),
					Request:    request,
				}, nil
			}
			body, marshalErr := json.Marshal(document)
			if marshalErr != nil {
				return nil, marshalErr
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(body))),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    request,
			}, nil
		})}
		return manager
	}

	stableStatus, err := newManager("stable").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stableStatus.ProductCount != 1 || stableStatus.VersionCount != 1 {
		t.Fatalf("unexpected stable status: %+v", stableStatus)
	}

	developmentStatus, err := newManager("development").Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if developmentStatus.ProductCount != 1 || developmentStatus.VersionCount != 1 ||
		developmentStatus.Channel != "development" {
		t.Fatalf("catalog was not reapplied for development channel: %+v", developmentStatus)
	}
	var development models.Software
	if err := db.Where("`key` = ? AND version = ?", "webserver", "1.31.0").
		First(&development).Error; err != nil {
		t.Fatal(err)
	}
	if !development.CatalogVisible || !development.Installable || !development.Recommended {
		t.Fatalf("development release is unavailable: %+v", development)
	}
	var state models.SoftwareCatalogState
	if err := db.First(&state, 1).Error; err != nil {
		t.Fatal(err)
	}
	if state.Channel != "development" {
		t.Fatalf("unexpected persisted channel: %q", state.Channel)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func openCatalogTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(t.TempDir()+"/catalog.db"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Software{}, &models.SoftwareCatalogState{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func signTestDocument(t *testing.T, privateKey ed25519.PrivateKey, products []Product) Document {
	t.Helper()
	generatedAt := time.Now().UTC().Truncate(time.Millisecond)
	unsigned := unsignedDocument{SchemaVersion: 1, GeneratedAt: generatedAt, Products: products}
	payload, err := json.Marshal(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	revision := hex.EncodeToString(digest[:])
	return Document{
		SchemaVersion: 1,
		Revision:      revision,
		GeneratedAt:   generatedAt,
		Products:      products,
		KeyID:         "test-key",
		Signature:     base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, signaturePayload(revision))),
	}
}
