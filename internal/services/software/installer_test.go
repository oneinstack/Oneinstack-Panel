package software

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"oneinstack/app"
	"oneinstack/config"
	"oneinstack/internal/models"
	"oneinstack/internal/services/script"
	"oneinstack/router/input"
)

func TestSetScriptParamsMapsGenericCatalogDatabasePassword(t *testing.T) {
	info := &script.ScriptInfo{
		Params: map[string]string{},
		ParameterSpecs: []script.ParameterSpec{
			{Name: "SOFTWARE_VERSION", Type: "string", Required: true},
			{Name: "DATABASE_PASSWORD", Type: "password", Secret: true},
		},
	}
	params := &input.InstallParams{
		Key:     "postgresql",
		Version: "18.1",
		Pwd:     "generated-secret-value",
	}

	NewInstaller().setScriptParams(info, params)

	if info.Params["DATABASE_PASSWORD"] != params.Pwd {
		t.Fatalf("DATABASE_PASSWORD was not mapped from the catalog password field")
	}
	if info.Params["SOFTWARE_VERSION"] != params.Version {
		t.Fatalf("SOFTWARE_VERSION = %q", info.Params["SOFTWARE_VERSION"])
	}
}

func TestSetScriptParamsDoesNotMapPasswordIntoUnrelatedSecret(t *testing.T) {
	info := &script.ScriptInfo{
		Params: map[string]string{},
		ParameterSpecs: []script.ParameterSpec{
			{Name: "CENTER_TOKEN", Type: "password", Secret: true},
		},
	}

	NewInstaller().setScriptParams(info, &input.InstallParams{
		Key: "demo", Version: "1.0", Pwd: "must-not-become-a-token",
	})

	if _, exists := info.Params["CENTER_TOKEN"]; exists {
		t.Fatal("generic password was mapped into an unrelated token parameter")
	}
}

func TestCenterManagedInstallDoesNotFallBackToLegacyScript(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "installer.db")); err != nil {
		t.Fatal(err)
	}
	originalConfig := app.ONE_CONFIG
	t.Cleanup(func() {
		app.ONE_CONFIG = originalConfig
	})
	app.ONE_CONFIG.ScriptCenter = config.ScriptCenter{
		Enabled:     false,
		CachePath:   filepath.Join(t.TempDir(), "cache"),
		BundledPath: filepath.Join(t.TempDir(), "missing"),
	}
	if err := app.DB().Create(&models.Software{
		Key:            "webserver",
		Name:           "Nginx",
		Version:        "1.31.0",
		Component:      "nginx",
		CatalogChannel: "stable",
		CatalogManaged: true,
		CatalogVisible: true,
		Installable:    true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	_, err := NewInstaller().getInstallScript(
		context.Background(),
		&input.InstallParams{Key: "webserver", Version: "1.31.0"},
		"install",
	)
	if err == nil {
		t.Fatal("Center-managed install unexpectedly used a legacy fallback")
	}
	if !strings.Contains(err.Error(), "resolve Center-managed nginx 1.31.0 installer") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "legacy fallback") {
		t.Fatalf("unexpected error: %v", err)
	}
}
