package website

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"oneinstack/internal/models"
)

func TestWebsiteSetEnabledRemovesAndRestoresVirtualHost(t *testing.T) {
	service := newLifecycleTestService(t)
	site := &models.Website{Domain: "switch.example.com", Type: "static", RootDir: "/switch"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(service.Publisher.ConfigDir, "switch.example.com.conf")
	if !site.Enabled {
		t.Fatal("new website is disabled")
	}
	if _, err := service.SetEnabled(context.Background(), site.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("disabled website config still exists: %v", err)
	}
	if _, err := service.SetEnabled(context.Background(), site.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("enabled website config was not restored: %v", err)
	}
}

func TestWebsiteLifecycleDisablesExpiredWebsite(t *testing.T) {
	service := newLifecycleTestService(t)
	expiresAt := time.Now().Add(time.Hour)
	site := &models.Website{
		Domain: "expired.example.com", Type: "static", RootDir: "/expired", ExpiresAt: &expiresAt,
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Minute)
	if err := service.DB.Model(&models.Website{}).Where("id = ?", site.ID).
		Update("expires_at", past).Error; err != nil {
		t.Fatal(err)
	}
	manager, err := NewLifecycleManager(service, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.runOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stored models.Website
	if err := service.DB.First(&stored, site.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled || stored.DisabledReason != WebsiteDisabledExpired {
		t.Fatalf("expired website was not disabled: %#v", stored)
	}
	if _, err := service.SetEnabled(context.Background(), site.ID, true); err == nil {
		t.Fatal("expired website was enabled without extending expiration")
	}
}

func TestWebsiteTrafficCollectorUsesCursorAndAggregatesByDay(t *testing.T) {
	service := newLifecycleTestService(t)
	site := &models.Website{Domain: "traffic.example.com", Type: "static", RootDir: "/traffic"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(service.LogRoot, "traffic_example_com_access.log")
	if err := os.MkdirAll(service.LogRoot, 0750); err != nil {
		t.Fatal(err)
	}
	first := `127.0.0.1 - - [01/Aug/2026:10:00:00 +0800] "GET / HTTP/1.1" 200 1024 "-" "test"` + "\n"
	if err := os.WriteFile(logPath, []byte(first), 0640); err != nil {
		t.Fatal(err)
	}
	if err := service.collectTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := service.collectTraffic(); err != nil {
		t.Fatal(err)
	}
	second := `127.0.0.1 - - [01/Aug/2026:10:01:00 +0800] "GET /asset HTTP/1.1" 200 2048 "-" "test"` + "\n"
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(second); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.collectTraffic(); err != nil {
		t.Fatal(err)
	}
	var traffic models.WebsiteTrafficDaily
	if err := service.DB.First(&traffic, "website_id = ? AND day = ?", site.ID, "2026-08-01").Error; err != nil {
		t.Fatal(err)
	}
	if traffic.BytesSent != 3072 || traffic.RequestCount != 2 {
		t.Fatalf("unexpected traffic aggregate: %#v", traffic)
	}
}

func newLifecycleTestService(t *testing.T) *Service {
	t.Helper()
	db := openWebsiteTestDB(t)
	if err := db.AutoMigrate(&models.WebsiteTrafficDaily{}, &models.WebsiteTrafficCursor{}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	return &Service{
		DB:      db,
		WebRoot: filepath.Join(root, "www"),
		LogRoot: filepath.Join(root, "logs"),
		Publisher: &Publisher{
			ConfigDir: filepath.Join(root, "conf"), NginxBinary: "nginx", Runner: &fakeNginxRunner{},
		},
	}
}
