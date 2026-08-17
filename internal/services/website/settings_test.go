package website

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oneinstack/internal/models"
)

func TestWebsiteSettingsRenderValidatedRuntimeDirectives(t *testing.T) {
	root := filepath.Join(t.TempDir(), "www", "example.com")
	record, err := (WebsiteSettings{
		RunningDirectory: "/public", DirectoryListing: true,
		DefaultDocuments: "index.php index.html", PHPBackend: "unix:/run/php/php8.3-fpm.sock",
		AllowedIPs: "192.168.1.0/24\n10.0.0.1", DeniedIPs: "203.0.113.8",
		RateLimitKB: 2048, RateLimitAfterKB: 512,
		RewriteRules:   "rewrite ^/old$ /new permanent;",
		Bindings:       []WebsiteDirectoryBinding{{Path: "/assets", Directory: "/public/assets", Enabled: true}},
		Redirects:      []WebsiteRedirectRule{{Source: "/legacy", Target: "https://example.com/new", Status: 301, Enabled: true}},
		ProxyRules:     []WebsiteProxyRule{{Path: "/api", Target: "http://127.0.0.1:9000", Host: "$host", Enabled: true}},
		HotlinkEnabled: true, HotlinkAllowEmpty: true, HotlinkDomains: "cdn.example.com",
		SecurityHeaders: true, DeniedPaths: "/.git\n/.env",
		AccessLogEnabled: true, ErrorLogEnabled: true,
	}).toModel(1)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := renderWebsiteSettings(&models.Website{Type: "php"}, root, record)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		filepath.Join(root, "public"), "index.php index.html", "on", "allow 192.168.1.0/24;",
		"deny 203.0.113.8;", "limit_rate 2048k;", "rewrite ^/old$ /new permanent;",
		"location ^~ /assets/", "location = /legacy", "proxy_pass http://127.0.0.1:9000;",
		"valid_referers none blocked server_names cdn.example.com;", "X-Content-Type-Options",
		"location ^~ /.git { return 403; }",
	} {
		combined := rendered.RootDir + rendered.DefaultDocuments + rendered.AutoIndex +
			rendered.RewriteDirectives + rendered.ServerDirectives + rendered.ExtraLocations
		if !strings.Contains(combined, expected) {
			t.Fatalf("rendered settings missing %q:\n%+v", expected, rendered)
		}
	}
}

func TestWebsiteUpdateSettingsPersistsAndPublishes(t *testing.T) {
	service := newLifecycleTestService(t)
	site := &models.Website{Domain: "settings.example.com", Type: "static", RootDir: "/settings"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(service.Publisher.ConfigDir, "settings.example.com.conf")
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	marker := "    # custom directive\n    add_header X-Manual retained always;\n"
	index := strings.LastIndex(string(before), "\n}")
	if index < 0 {
		t.Fatal("generated config has no server closing brace")
	}
	if err := os.WriteFile(configPath, []byte(string(before[:index])+"\n"+marker+string(before[index:])), 0640); err != nil {
		t.Fatal(err)
	}
	document, err := service.UpdateSettings(context.Background(), site.ID, WebsiteSettings{
		DefaultDocuments: "home.html index.html", DirectoryListing: true,
		PHPBackend: "unix:/dev/shm/php-cgi.sock", SecurityHeaders: true,
		AccessLogEnabled: true, ErrorLogEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if document.Settings.DefaultDocuments != "home.html index.html" {
		t.Fatalf("settings were not returned: %#v", document.Settings)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "index home.html index.html;") ||
		!strings.Contains(string(content), "autoindex on;") ||
		!strings.Contains(string(content), "X-Frame-Options") ||
		!strings.Contains(string(content), "add_header X-Manual retained always;") {
		t.Fatalf("published config did not include settings:\n%s", content)
	}
	stored, err := service.GetSettings(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Settings.DefaultDocuments != "home.html index.html" || !stored.Settings.DirectoryListing {
		t.Fatalf("stored settings are incorrect: %#v", stored.Settings)
	}
}

func TestWebsiteTamperProtectionRestoresManagedConfig(t *testing.T) {
	service := newLifecycleTestService(t)
	site := &models.Website{Domain: "protected.example.com", Type: "static", RootDir: "/protected"}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	_, err := service.UpdateSettings(context.Background(), site.ID, WebsiteSettings{
		DefaultDocuments: "index.html", PHPBackend: "unix:/dev/shm/php-cgi.sock",
		SecurityHeaders: true, AccessLogEnabled: true, ErrorLogEnabled: true,
		TamperProtection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(service.Publisher.ConfigDir, "protected.example.com.conf")
	if err := os.WriteFile(configPath, []byte("tampered\n"), 0640); err != nil {
		t.Fatal(err)
	}
	if err := service.enforceTamperProtection(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) == "tampered\n" || !strings.Contains(string(content), "Managed by OneinStack Panel") {
		t.Fatalf("protected config was not restored:\n%s", content)
	}
}

func TestReadManagedConfigRepairsMissingEnabledWebsiteConfig(t *testing.T) {
	service := newLifecycleTestService(t)
	configRoot := filepath.Dir(service.Publisher.ConfigDir)
	mainConfig := filepath.Join(configRoot, "nginx.conf")
	if err := os.WriteFile(mainConfig, []byte("events {}\nhttp { include conf/*.conf; }\n"), 0640); err != nil {
		t.Fatal(err)
	}
	service.ConfigManager = &WebServerConfigManager{
		Server: WebServerInfo{
			Available:              true,
			Name:                   "Nginx",
			BinaryPath:             filepath.Join(configRoot, "sbin", "nginx"),
			Prefix:                 configRoot,
			ConfigRoot:             configRoot,
			MainConfigPath:         mainConfig,
			SiteConfigDir:          service.Publisher.ConfigDir,
			ConfigurationAvailable: true,
		},
		Runner:     &fakeNginxRunner{},
		BackupRoot: filepath.Join(configRoot, "backups"),
	}
	site := &models.Website{
		Domain: "repair.example.com", Type: "static", RootDir: "/repair",
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(service.Publisher.ConfigDir, "repair.example.com.conf")
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	document, err := service.ReadManagedConfig(context.Background(), site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if document.Path != "conf/repair.example.com.conf" ||
		!strings.Contains(document.Content, "server_name repair.example.com;") {
		t.Fatalf("unexpected repaired website configuration: %#v", document)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("repaired configuration was not published: %v", err)
	}
}

func TestRestoreMissingManagedConfigsAfterWebServerReinstall(t *testing.T) {
	service := newLifecycleTestService(t)
	preserved := &models.Website{
		Domain: "preserved.example.com", Type: "static", RootDir: "/preserved",
	}
	restored := &models.Website{
		Domain: "restored.example.com", Type: "static", RootDir: "/restored",
	}
	disabled := &models.Website{
		Domain: "disabled.example.com", Type: "static", RootDir: "/disabled",
	}
	for _, site := range []*models.Website{preserved, restored, disabled} {
		if err := service.Add(context.Background(), site); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.SetEnabled(context.Background(), disabled.ID, false); err != nil {
		t.Fatal(err)
	}

	preservedPath := filepath.Join(service.Publisher.ConfigDir, "preserved.example.com.conf")
	restoredPath := filepath.Join(service.Publisher.ConfigDir, "restored.example.com.conf")
	disabledPath := filepath.Join(service.Publisher.ConfigDir, "disabled.example.com.conf")
	customContent := "# retain the existing runtime configuration\n"
	if err := os.WriteFile(preservedPath, []byte(customContent), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(restoredPath); err != nil {
		t.Fatal(err)
	}

	count, err := service.RestoreMissingManagedConfigs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restored config count = %d, want 1", count)
	}
	preservedContent, err := os.ReadFile(preservedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(preservedContent) != customContent {
		t.Fatalf("existing website configuration was overwritten:\n%s", preservedContent)
	}
	restoredContent, err := os.ReadFile(restoredPath)
	if err != nil {
		t.Fatalf("missing enabled website configuration was not restored: %v", err)
	}
	if !strings.Contains(string(restoredContent), "server_name restored.example.com;") {
		t.Fatalf("unexpected restored website configuration:\n%s", restoredContent)
	}
	if _, err := os.Stat(disabledPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disabled website configuration was restored: %v", err)
	}
}

func TestWebsiteSettingsRejectUnsafeDirectivesAndTargets(t *testing.T) {
	cases := []WebsiteSettings{
		{DefaultDocuments: "index.html; include", PHPBackend: "unix:/dev/shm/php-cgi.sock"},
		{DefaultDocuments: "index.html", PHPBackend: "unix:/tmp/php.sock;include x"},
		{DefaultDocuments: "index.html", PHPBackend: "unix:/dev/shm/php-cgi.sock", RewriteRules: "include /tmp/evil;"},
		{DefaultDocuments: "index.html", PHPBackend: "unix:/dev/shm/php-cgi.sock", ProxyRules: []WebsiteProxyRule{{Path: "/api", Target: "file:///etc/passwd", Enabled: true}}},
	}
	for i, settings := range cases {
		record, err := settings.toModel(1)
		if err != nil {
			t.Fatalf("case %d encoding failed: %v", i, err)
		}
		if _, err := renderWebsiteSettings(&models.Website{Type: "php"}, "/data/wwwroot/example", record); err == nil {
			t.Fatalf("unsafe settings case %d was accepted: %#v", i, settings)
		}
	}
}
