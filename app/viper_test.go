package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigCreatesSecureDefault(t *testing.T) {
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if ONE_CONFIG.System.Port != "8089" {
		t.Fatalf("expected default port 8089, got %q", ONE_CONFIG.System.Port)
	}
	if ONE_CONFIG.System.BindAddress != "0.0.0.0" ||
		ONE_CONFIG.System.HTTPSEnabled ||
		ONE_CONFIG.System.HTTPSPort != "8443" ||
		len(ONE_CONFIG.System.TrustedProxies) != 0 {
		t.Fatalf("unexpected panel network defaults: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.FileUploadMaxBytes != 100<<20 {
		t.Fatalf("unexpected upload limit: %d", ONE_CONFIG.System.FileUploadMaxBytes)
	}
	if ONE_CONFIG.System.FileMinFreeBytes != 1<<30 {
		t.Fatalf("unexpected minimum free space: %d", ONE_CONFIG.System.FileMinFreeBytes)
	}
	if ONE_CONFIG.System.TrashRetentionDays != 30 || ONE_CONFIG.System.TrashCleanupSchedule != "0 3 * * *" {
		t.Fatalf("unexpected trash policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.SoftwareTaskRetentionDays != 90 ||
		ONE_CONFIG.System.SoftwareTaskLogRetentionDays != 30 ||
		ONE_CONFIG.System.SoftwareTaskCleanupSchedule != "30 3 * * *" {
		t.Fatalf("unexpected software task retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.DatabaseBackupRetentionDays != 30 ||
		ONE_CONFIG.System.DatabaseBackupCleanupSchedule != "0 4 * * *" {
		t.Fatalf("unexpected database backup retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.WebsiteBackupRetentionDays != 30 ||
		ONE_CONFIG.System.WebsiteBackupCleanupSchedule != "15 4 * * *" ||
		ONE_CONFIG.System.WebsiteBackupMaxBytes != int64(20<<30) ||
		ONE_CONFIG.System.WebsiteBackupMaxFiles != 200000 {
		t.Fatalf("unexpected website backup retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.AuditRetentionDays != 365 ||
		ONE_CONFIG.System.AuditCleanupSchedule != "45 4 * * *" ||
		ONE_CONFIG.System.AuditExportMaxRows != 10000 {
		t.Fatalf("unexpected audit retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.MonitorSampleSchedule != "*/1 * * * *" ||
		ONE_CONFIG.System.MonitorRetentionDays != 30 ||
		ONE_CONFIG.System.MonitorAlertRetentionDays != 365 ||
		ONE_CONFIG.System.MonitorCleanupSchedule != "20 4 * * *" {
		t.Fatalf("unexpected monitor retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.RuntimeLogRetentionDays != 30 ||
		ONE_CONFIG.System.RuntimeLogCleanupSchedule != "10 5 * * *" {
		t.Fatalf("unexpected runtime log retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.CronExecutionRetentionDays != 30 ||
		ONE_CONFIG.System.CronExecutionCleanupSchedule != "25 5 * * *" {
		t.Fatalf("unexpected cron execution retention policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.ACMERenewBeforeDays != 30 ||
		ONE_CONFIG.System.CertificateExpiryWarningDays != 30 ||
		ONE_CONFIG.System.ACMEIssueTimeoutMinutes != 15 ||
		!strings.HasSuffix(ONE_CONFIG.System.CertificatePath, "certificates") ||
		!strings.HasSuffix(ONE_CONFIG.System.ACMEChallengePath, "acme-webroot") {
		t.Fatalf("unexpected certificate policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.System.TerminalEnabled || ONE_CONFIG.System.TerminalSessionMins != 15 {
		t.Fatalf("unexpected terminal policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.UpdateCenter.Enabled ||
		ONE_CONFIG.UpdateCenter.Channel != "stable" ||
		ONE_CONFIG.UpdateCenter.RequestTimeoutSeconds != 20 ||
		ONE_CONFIG.UpdateCenter.MaxPackageBytes != 256<<20 ||
		ONE_CONFIG.UpdateCenter.MaxExpandedBytes != 512<<20 ||
		ONE_CONFIG.UpdateCenter.HealthTimeoutSeconds != 60 ||
		ONE_CONFIG.UpdateCenter.BackupRetention != 5 {
		t.Fatalf("unexpected update center defaults: %+v", ONE_CONFIG.UpdateCenter)
	}
	if len(ONE_CONFIG.System.JWTSecret) != 64 ||
		ONE_CONFIG.System.JWTSecret == legacyInsecureJWTSecret {
		t.Fatalf("JWT secret was not generated securely")
	}
	if len(ONE_CONFIG.System.CredentialKey) != 64 {
		t.Fatalf("credential key was not generated securely")
	}
	generatedSecret := ONE_CONFIG.System.JWTSecret
	generatedCredentialKey := ONE_CONFIG.System.CredentialKey
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if ONE_CONFIG.System.JWTSecret != generatedSecret {
		t.Fatal("JWT secret changed during a normal config reload")
	}
	if ONE_CONFIG.System.CredentialKey != generatedCredentialKey {
		t.Fatal("credential key changed during a normal config reload")
	}

	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0600 {
		t.Fatalf("expected config permissions 0600, got %04o", permissions)
	}
}

func TestLoadConfigRotatesLegacyJWTSecret(t *testing.T) {
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	legacyConfig := defaultConfig + `    jwtSecret: "` + legacyInsecureJWTSecret + "\"\n"
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if ONE_CONFIG.System.JWTSecret == legacyInsecureJWTSecret ||
		len(ONE_CONFIG.System.JWTSecret) != 64 {
		t.Fatal("legacy JWT secret was not rotated")
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), legacyInsecureJWTSecret) {
		t.Fatal("legacy JWT secret remains in the config file")
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("rotated config permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestLoadConfigSupportsEnvironmentOverrides(t *testing.T) {
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	t.Setenv("ONEINSTACK_SYSTEM_PORT", "9090")
	t.Setenv("ONEINSTACK_SYSTEM_FILE_ROOT_QUOTA_BYTES", "2147483648")
	t.Setenv("ONEINSTACK_SYSTEM_AUDIT_RETENTION_DAYS", "730")
	t.Setenv("ONEINSTACK_SYSTEM_AUDIT_EXPORT_MAX_ROWS", "25000")
	t.Setenv("ONEINSTACK_UPDATE_CENTER_BACKUP_RETENTION", "7")
	t.Setenv("ONEINSTACK_UPDATE_CENTER_ENABLED", "true")
	t.Setenv("ONEINSTACK_UPDATE_CENTER_URL", "http://127.0.0.1:8189")
	t.Setenv("ONEINSTACK_SCRIPT_CENTER_ENABLED", "true")
	t.Setenv("ONEINSTACK_SCRIPT_CENTER_ALLOW_INSECURE_HTTP", "true")
	t.Setenv("ONEINSTACK_SCRIPT_CENTER_URL", "http://center.internal:8189")
	t.Setenv(
		"ONEINSTACK_SCRIPT_CENTER_TRUSTED_KEYS",
		`{"test-key":"emRWTpCtx2AyS28X2CxVxhWsJd74pPZ5gMOm7cL6Rm0="}`,
	)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, err := LoadConfig(configPath); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if ONE_CONFIG.System.Port != "9090" {
		t.Fatalf("expected environment port 9090, got %q", ONE_CONFIG.System.Port)
	}
	if ONE_CONFIG.System.FileRootQuotaBytes != 2147483648 {
		t.Fatalf("expected environment file quota, got %d", ONE_CONFIG.System.FileRootQuotaBytes)
	}
	if ONE_CONFIG.System.AuditRetentionDays != 730 || ONE_CONFIG.System.AuditExportMaxRows != 25000 {
		t.Fatalf("unexpected environment audit policy: %+v", ONE_CONFIG.System)
	}
	if ONE_CONFIG.UpdateCenter.BackupRetention != 7 ||
		!ONE_CONFIG.UpdateCenter.Enabled ||
		ONE_CONFIG.UpdateCenter.CenterURL != "http://127.0.0.1:8189" {
		t.Fatalf("unexpected environment update center config: %+v", ONE_CONFIG.UpdateCenter)
	}
	if !ONE_CONFIG.ScriptCenter.Enabled || !ONE_CONFIG.ScriptCenter.AllowInsecureHTTP ||
		ONE_CONFIG.ScriptCenter.URL != "http://center.internal:8189" ||
		ONE_CONFIG.ScriptCenter.TrustedKeys["test-key"] == "" {
		t.Fatalf("unexpected script center environment config: %+v", ONE_CONFIG.ScriptCenter)
	}
}
