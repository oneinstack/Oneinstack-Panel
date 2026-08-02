package website

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"oneinstack/internal/models"
	safeservice "oneinstack/internal/services/safe"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type runnerResult struct {
	output string
	err    error
}

type fakeNginxRunner struct {
	mu      sync.Mutex
	results []runnerResult
	calls   [][]string
}

type fakeWebsiteFirewallRunner struct {
	commands []string
}

func (runner *fakeWebsiteFirewallRunner) LookPath(name string) (string, error) {
	if name == "ufw" {
		return "/usr/sbin/ufw", nil
	}
	return "", errors.New("not found")
}

func (runner *fakeWebsiteFirewallRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	runner.commands = append(runner.commands, call)
	if call == "ufw status" {
		return []byte("Status: active"), nil
	}
	return nil, nil
}

func (runner *fakeNginxRunner) Run(
	_ context.Context,
	command string,
	args ...string,
) ([]byte, error) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	call := append([]string{command}, args...)
	runner.calls = append(runner.calls, call)
	if len(runner.results) == 0 {
		return nil, nil
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return []byte(result.output), result.err
}

func TestPrepareWebsiteNormalizesDomainsPathsAndProxy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "www")
	logRoot := filepath.Join(t.TempDir(), "logs")
	site, err := prepareWebsite(&models.Website{
		Domain:  "Example.COM:8080, www.example.com:8080",
		Type:    "static",
		RootDir: "/example.com",
		Remark:  "first line\nsecond line",
	}, root, logRoot)
	if err != nil {
		t.Fatal(err)
	}
	if site.model.Name != "example.com" ||
		site.model.Domain != "example.com,www.example.com" ||
		site.model.RootDir != filepath.Join(root, "example.com") {
		t.Fatalf("unexpected normalized website: %#v", site.model)
	}
	if !strings.Contains(site.config, "listen 8080;") ||
		!strings.Contains(site.config, "server_name example.com www.example.com;") ||
		!strings.Contains(site.config, "# first line second line") {
		t.Fatalf("unexpected rendered config:\n%s", site.config)
	}

	proxy, err := prepareWebsite(&models.Website{
		Domain:  "api.example.com",
		Type:    "proxy",
		Pact:    "https://",
		SendUrl: "127.0.0.1:9443/api",
		TarUrl:  "$http_host",
	}, root, logRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(proxy.config, "proxy_pass https://127.0.0.1:9443/api;") ||
		!strings.Contains(proxy.config, "proxy_set_header Host $http_host;") {
		t.Fatalf("unexpected proxy config:\n%s", proxy.config)
	}
}

func TestPrepareWebsiteRejectsNginxInjectionAndTraversal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "www")
	logRoot := filepath.Join(t.TempDir(), "logs")
	cases := []*models.Website{
		{Domain: "example.com;include /tmp/evil", Type: "static", RootDir: "/example"},
		{Domain: "example.com", Type: "static", RootDir: "../../etc"},
		{Domain: "example.com", Type: "proxy", Pact: "file", SendUrl: "/etc/passwd"},
		{Domain: "example.com", Type: "proxy", Pact: "http", SendUrl: "127.0.0.1:80;\ninclude x"},
		{Domain: "example.com", Type: "proxy", Pact: "http", SendUrl: "127.0.0.1:80", TarUrl: "$request_uri"},
	}
	for i, candidate := range cases {
		if _, err := prepareWebsite(candidate, root, logRoot); err == nil {
			t.Fatalf("case %d unexpectedly passed validation: %#v", i, candidate)
		}
	}
}

func TestPrepareWebsiteRendersACMEChallengeTLSAndRedirect(t *testing.T) {
	root := filepath.Join(t.TempDir(), "www")
	logRoot := filepath.Join(t.TempDir(), "logs")
	challengeRoot := filepath.Join(t.TempDir(), "acme")
	certificateRoot := filepath.Join(t.TempDir(), "certificates")
	site, err := prepareWebsiteWithTLS(
		&models.Website{
			Domain:  "example.com,www.example.com",
			Type:    "static",
			RootDir: "/example.com",
		},
		root,
		logRoot,
		challengeRoot,
		TLSOptions{
			Enabled:    true,
			ForceHTTPS: true,
			CertPath:   filepath.Join(certificateRoot, "fullchain.pem"),
			KeyPath:    filepath.Join(certificateRoot, "privkey.pem"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"location ^~ /.well-known/acme-challenge/",
		"root " + challengeRoot + ";",
		"return 301 https://$host$request_uri;",
		"listen 443 ssl http2;",
		"ssl_certificate " + filepath.Join(certificateRoot, "fullchain.pem") + ";",
		"ssl_certificate_key " + filepath.Join(certificateRoot, "privkey.pem") + ";",
		"ssl_protocols TLSv1.2 TLSv1.3;",
		`add_header Strict-Transport-Security "max-age=31536000" always;`,
	} {
		if !strings.Contains(site.config, expected) {
			t.Fatalf("TLS config is missing %q:\n%s", expected, site.config)
		}
	}
}

func TestPublisherRestoresOldConfigWhenNginxTestFails(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "example.com.conf")
	if err := os.WriteFile(configPath, []byte("old config\n"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeNginxRunner{results: []runnerResult{
		{output: "nginx: invalid directive", err: errors.New("exit status 1")},
		{},
		{},
	}}
	publisher := &Publisher{ConfigDir: configDir, NginxBinary: "nginx", Runner: runner}
	replacement := "invalid new config\n"
	if _, err := publisher.Publish(context.Background(), map[string]*string{
		"example.com.conf": &replacement,
	}); err == nil {
		t.Fatal("invalid Nginx config publication unexpectedly succeeded")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old config\n" {
		t.Fatalf("old config was not restored: %q", content)
	}
	if len(runner.calls) != 3 ||
		len(runner.calls[0]) != 2 || runner.calls[0][1] != "-t" ||
		len(runner.calls[2]) != 3 || runner.calls[2][1] != "-s" {
		t.Fatalf("unexpected Nginx recovery calls: %#v", runner.calls)
	}
}

func TestPublisherCompensatesSuccessfulPublication(t *testing.T) {
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "example.com.conf")
	if err := os.WriteFile(configPath, []byte("old config\n"), 0640); err != nil {
		t.Fatal(err)
	}
	runner := &fakeNginxRunner{}
	publisher := &Publisher{ConfigDir: configDir, NginxBinary: "nginx", Runner: runner}
	replacement := "new config\n"
	publication, err := publisher.Publish(context.Background(), map[string]*string{
		"example.com.conf": &replacement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := publication.Rollback(context.Background()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "old config\n" {
		t.Fatalf("publication rollback did not restore old config: %q", content)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("Nginx test/reload calls = %d, want 4", len(runner.calls))
	}
}

func TestWebsiteServiceLifecyclePreservesWebsiteFiles(t *testing.T) {
	db := openWebsiteTestDB(t)
	webRoot := filepath.Join(t.TempDir(), "www")
	logRoot := filepath.Join(t.TempDir(), "logs")
	configDir := t.TempDir()
	service := &Service{
		DB:      db,
		WebRoot: webRoot,
		LogRoot: logRoot,
		Publisher: &Publisher{
			ConfigDir:   configDir,
			NginxBinary: "nginx",
			Runner:      &fakeNginxRunner{},
		},
	}
	site := &models.Website{
		Domain:  "example.com",
		Type:    "static",
		RootDir: "/example.com",
		Remark:  "production site",
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if site.ID == 0 {
		t.Fatal("website ID was not persisted")
	}
	configPath := filepath.Join(configDir, "example.com.conf")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("site config was not published: %v", err)
	}
	sentinel := filepath.Join(site.RootDir, "index.html")
	defaultPage, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("default website page was not created: %v", err)
	}
	if !strings.Contains(string(defaultPage), "OneinStack") ||
		!strings.Contains(string(defaultPage), "example.com") {
		t.Fatalf("unexpected default website page: %s", defaultPage)
	}
	if err := os.WriteFile(sentinel, []byte("keep me"), 0640); err != nil {
		t.Fatal(err)
	}

	site.Domain = "www.example.com"
	site.RootDir = "/example.com"
	if err := service.Update(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old site config still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "www.example.com.conf")); err != nil {
		t.Fatalf("renamed site config was not published: %v", err)
	}

	if err := service.Delete(context.Background(), site.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("website files were deleted with vhost: %v", err)
	}
	var count int64
	if err := db.Model(&models.Website{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("website rows = %d, want 0", count)
	}
}

func TestWebsiteServiceCreatesDefaultPageAndOpensCustomPort(t *testing.T) {
	db := openWebsiteTestDB(t)
	if err := db.AutoMigrate(&models.IptablesRule{}); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(t.TempDir(), "www")
	firewallRunner := &fakeWebsiteFirewallRunner{}
	service := &Service{
		DB: db, WebRoot: webRoot, LogRoot: filepath.Join(t.TempDir(), "logs"),
		Publisher: &Publisher{
			ConfigDir: t.TempDir(), NginxBinary: "nginx", Runner: &fakeNginxRunner{},
		},
		Firewall: safeservice.NewService(db, firewallRunner, 8089),
	}
	site := &models.Website{
		Domain: "custom.example.com:8181", Type: "static", RootDir: "/custom",
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(site.RootDir, "index.html"))
	if err != nil || !strings.Contains(string(page), "网站已经准备就绪") {
		t.Fatalf("default page missing: %v %q", err, page)
	}
	var rule models.IptablesRule
	if err := db.Where("ports = ?", "8181").First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if rule.Remark != "custom.example.com" || rule.State != 1 || rule.Protocol != "tcp" {
		t.Fatalf("unexpected website firewall rule: %#v", rule)
	}
	foundCommand := false
	for _, command := range firewallRunner.commands {
		if strings.Contains(command, "port 8181") {
			foundCommand = true
			break
		}
	}
	if !foundCommand {
		t.Fatalf("website port was not applied to UFW: %#v", firewallRunner.commands)
	}
}

func TestWebsiteServiceRollsBackDatabaseAndNewDirectoryOnConfigFailure(t *testing.T) {
	db := openWebsiteTestDB(t)
	if err := db.AutoMigrate(&models.IptablesRule{}); err != nil {
		t.Fatal(err)
	}
	webRoot := filepath.Join(t.TempDir(), "www")
	runner := &fakeNginxRunner{results: []runnerResult{
		{output: "nginx validation failed", err: errors.New("exit 1")},
		{},
		{},
	}}
	firewallRunner := &fakeWebsiteFirewallRunner{}
	service := &Service{
		DB:      db,
		WebRoot: webRoot,
		LogRoot: filepath.Join(t.TempDir(), "logs"),
		Publisher: &Publisher{
			ConfigDir:   t.TempDir(),
			NginxBinary: "nginx",
			Runner:      runner,
		},
		Firewall: safeservice.NewService(db, firewallRunner, 8089),
	}
	site := &models.Website{
		Domain:  "broken.example.com",
		Type:    "static",
		RootDir: "/broken.example.com",
	}
	if err := service.Add(context.Background(), site); err == nil {
		t.Fatal("website with invalid Nginx publication unexpectedly succeeded")
	}
	var count int64
	if err := db.Model(&models.Website{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back website rows = %d, want 0", count)
	}
	if err := db.Model(&models.IptablesRule{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back firewall rules = %d, want 0", count)
	}
	if _, err := os.Stat(filepath.Join(webRoot, "broken.example.com")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new empty root was not compensated: %v", err)
	}
	removedPort := false
	for _, command := range firewallRunner.commands {
		if strings.Contains(command, "delete allow") && strings.Contains(command, "port 80") {
			removedPort = true
			break
		}
	}
	if !removedPort {
		t.Fatalf("website firewall rule was not compensated: %#v", firewallRunner.commands)
	}
}

func TestWebsiteDeleteWithFilesRestoresDirectoryWhenNginxPublicationFails(t *testing.T) {
	db := openWebsiteTestDB(t)
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	logRoot := filepath.Join(root, "logs")
	configDir := filepath.Join(root, "nginx")
	certificateRoot := filepath.Join(root, "certificates")
	for _, directory := range []string{webRoot, logRoot, configDir, certificateRoot} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	runner := &fakeNginxRunner{}
	service := &Service{
		DB: db, WebRoot: webRoot, LogRoot: logRoot,
		CertificateRoot: certificateRoot,
		Publisher: &Publisher{
			ConfigDir: configDir, NginxBinary: "nginx", Runner: runner,
		},
	}
	site := &models.Website{
		Domain: "rollback.example.com", Type: "static", RootDir: "/rollback",
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(site.RootDir, "index.html")
	if err := os.WriteFile(sentinel, []byte("must survive"), 0640); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.results = []runnerResult{
		{output: "nginx validation failed", err: errors.New("exit 1")},
		{},
		{},
	}
	runner.mu.Unlock()
	if err := service.DeleteWithOptions(context.Background(), site.ID, true); err == nil {
		t.Fatal("website delete unexpectedly succeeded")
	}
	if value, err := os.ReadFile(sentinel); err != nil || string(value) != "must survive" {
		t.Fatalf("website directory was not restored: %q, %v", value, err)
	}
	var stored models.Website
	if err := db.First(&stored, site.ID).Error; err != nil {
		t.Fatalf("website database row was not restored: %v", err)
	}
}

func TestWebsiteDeleteRejectsStoredSymlinkRoot(t *testing.T) {
	db := openWebsiteTestDB(t)
	root := t.TempDir()
	webRoot := filepath.Join(root, "www")
	logRoot := filepath.Join(root, "logs")
	configDir := filepath.Join(root, "nginx")
	certificateRoot := filepath.Join(root, "certificates")
	for _, directory := range []string{webRoot, logRoot, configDir, certificateRoot} {
		if err := os.MkdirAll(directory, 0750); err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{
		DB: db, WebRoot: webRoot, LogRoot: logRoot,
		CertificateRoot: certificateRoot,
		Publisher: &Publisher{
			ConfigDir: configDir, NginxBinary: "nginx", Runner: &fakeNginxRunner{},
		},
	}
	site := &models.Website{
		Domain: "symlink.example.com", Type: "static", RootDir: "/symlink",
	}
	if err := service.Add(context.Background(), site); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.MkdirAll(outside, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(site.RootDir, "index.html")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(site.RootDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, site.RootDir); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteWithOptions(context.Background(), site.ID, true); err == nil ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("stored symlink root was not rejected: %v", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside directory was changed: %v", err)
	}
}

func openWebsiteTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "website.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Website{},
		&models.WebsiteSetting{},
		&models.WebsiteTrafficDaily{},
		&models.WebsiteTrafficCursor{},
		&models.Certificate{},
		&models.CertificateTask{},
		&models.CertificateOperationLock{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}
