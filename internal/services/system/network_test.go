package system

import (
	"oneinstack/app"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	panelServer "oneinstack/server"
)

func TestNormalizePanelConfigUsesIPHTTPDefaults(t *testing.T) {
	config := normalizePanelConfig(panelServer.PanelConfig{
		HTTPPort:       " 8089 ",
		TrustedProxies: []string{" 127.0.0.1 ", "127.0.0.1"},
	})
	if config.BindAddress != "0.0.0.0" || config.HTTPPort != "8089" || config.HTTPSPort != "8443" {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if !reflect.DeepEqual(config.TrustedProxies, []string{"127.0.0.1"}) {
		t.Fatalf("trusted proxies were not normalized: %+v", config.TrustedProxies)
	}
}

func TestNormalizeManagedPanelConfigNormalizesEntryPath(t *testing.T) {
	config := normalizeManagedPanelConfig(managedPanelConfig{
		Network:        panelServer.PanelConfig{HTTPPort: "8089"},
		PanelEntryPath: "  demo-entry/ ",
	})
	if config.PanelEntryPath != "/demo-entry" {
		t.Fatalf("unexpected normalized panel entry path %q", config.PanelEntryPath)
	}
}

func TestPersistPanelConfigAtomicallyKeepsSecurePermissions(t *testing.T) {
	originalConfig := app.ONE_CONFIG
	originalViper := app.ONE_VIP
	t.Cleanup(func() {
		app.ONE_CONFIG = originalConfig
		app.ONE_VIP = originalViper
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := app.LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	before = []byte(strings.Replace(string(before), "fileuploadmaxbytes:", "fileUploadMaxBytes:", 1))
	if err := os.WriteFile(configPath, append([]byte("# keep-this-comment\n"), before...), 0600); err != nil {
		t.Fatal(err)
	}
	next := effectivePanelConfig()
	next.HTTPPort = "18089"
	next.TrustedProxies = []string{"127.0.0.1"}
	if err := persistPanelConfig(next); err != nil {
		t.Fatalf("persistPanelConfig: %v", err)
	}
	stored, err := loadStoredPanelConfig()
	if err != nil {
		t.Fatalf("loadStoredPanelConfig: %v", err)
	}
	if stored.HTTPPort != "18089" || !reflect.DeepEqual(stored.TrustedProxies, []string{"127.0.0.1"}) {
		t.Fatalf("unexpected stored config: %+v", stored)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("config permissions = %04o", info.Mode().Perm())
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# keep-this-comment") ||
		!strings.Contains(string(raw), "fileUploadMaxBytes:") ||
		!strings.Contains(string(raw), "bindAddress:") {
		t.Fatalf("configuration key spelling was not preserved:\n%s", raw)
	}
}

func TestAccessURLUsesServerIPPlaceholderForAllInterfaces(t *testing.T) {
	if got := accessURL("http", "0.0.0.0", "8089"); got != "http://服务器IP:8089" {
		t.Fatalf("unexpected access URL %q", got)
	}
	if got := accessURL("https", "2001:db8::10", "8443"); got != "https://[2001:db8::10]:8443" {
		t.Fatalf("unexpected IPv6 access URL %q", got)
	}
}

func TestValidateManagedPanelConfigRejectsReservedEntryPath(t *testing.T) {
	err := validateManagedPanelConfig(managedPanelConfig{
		Network: panelServer.PanelConfig{
			BindAddress: "0.0.0.0",
			HTTPPort:    "8089",
			HTTPSPort:   "8443",
		},
		PanelEntryEnabled: true,
		PanelEntryPath:    "/v1",
	})
	if err == nil || !strings.Contains(err.Error(), "reserved path") {
		t.Fatalf("expected reserved path error, got %v", err)
	}
}

func TestPanelAccessURLUsesEntryPathWhenEnabled(t *testing.T) {
	config := managedPanelConfig{
		Network: panelServer.PanelConfig{
			BindAddress:  "0.0.0.0",
			HTTPPort:     "8089",
			HTTPSEnabled: true,
			HTTPSPort:    "8443",
		},
		PanelEntryEnabled: true,
		PanelEntryPath:    "/AbCd123456",
	}
	if got := panelAccessURL(config); got != "https://服务器IP:8443/AbCd123456" {
		t.Fatalf("unexpected panel access URL %q", got)
	}
}
