package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"oneinstack/utils"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/viper"
)

const legacyInsecureJWTSecret = "a1b2c3d4e5f6789012345678901234567890abcdef1234567890abcdef123456"

const defaultConfig = `scriptCenter:
    enabled: false
    allowInsecureHTTP: false
    url: "https://scripts.example.com"
    channel: "stable"
    requestTimeoutSeconds: 10
    maxPackageBytes: 67108864
    maxExpandedBytes: 268435456
    cachePath: "/usr/local/one/script-registry/cache"
    bundledPath: "/usr/local/one/script-registry/bundled"
    catalogSyncIntervalMinutes: 15
    catalogStaleAfterHours: 24
    trustedKeys: {}
updateCenter:
    enabled: false
    centerUrl: ""
    manifestUrl: "https://updates.example.com/oneinstack/stable/manifest.json"
    channel: "stable"
    requestTimeoutSeconds: 20
    maxPackageBytes: 268435456
    maxExpandedBytes: 536870912
    healthTimeoutSeconds: 60
    backupRetention: 5
    trustedKeys: {}
system:
    port: "8089"
    bindAddress: "0.0.0.0"
    httpsEnabled: false
    httpsPort: "8443"
    httpsCertificateFile: ""
    httpsPrivateKeyFile: ""
    trustedProxies: []
    panelEntryEnabled: false
    panelEntryPath: ""
    remote: ""
    defaultPath: "/"
    webPath: "/data/wwwroot/"
    logPath: "/data/wwwlogs/"
    dataPath: "/data/db/"
    credentialKey: ""
    fileUploadMaxBytes: 104857600
    fileEditMaxBytes: 10485760
    fileRootQuotaBytes: 0
    fileMinFreeBytes: 1073741824
    trashRetentionDays: 30
    trashCleanupSchedule: "0 3 * * *"
    softwareTaskRetentionDays: 90
    softwareTaskLogRetentionDays: 30
    softwareTaskCleanupSchedule: "30 3 * * *"
    databaseBackupRetentionDays: 30
    databaseBackupCleanupSchedule: "0 4 * * *"
    websiteBackupRetentionDays: 30
    websiteBackupCleanupSchedule: "15 4 * * *"
    websiteBackupMaxBytes: 21474836480
    websiteBackupMaxFiles: 200000
    auditRetentionDays: 365
    auditCleanupSchedule: "45 4 * * *"
    auditExportMaxRows: 10000
    monitorSampleSchedule: "*/1 * * * *"
    monitorRetentionDays: 30
    monitorAlertRetentionDays: 365
    monitorCleanupSchedule: "20 4 * * *"
    runtimeLogRetentionDays: 30
    runtimeLogCleanupSchedule: "10 5 * * *"
    cronExecutionRetentionDays: 30
    cronExecutionCleanupSchedule: "25 5 * * *"
    certificatePath: "/usr/local/one/certificates"
    acmeChallengePath: "/usr/local/one/acme-webroot"
    acmeDirectoryUrl: "https://acme-v02.api.letsencrypt.org/directory"
    acmeRenewSchedule: "15 3 * * *"
    acmeRenewBeforeDays: 30
    certificateExpiryWarningDays: 30
    acmeIssueTimeoutMinutes: 15
    terminalEnabled: false
    terminalSessionMinutes: 15
    allowInsecureWebSocketInDev: false
    allowInlineStyle: false
    terminalIdleMinutes: 5
    terminalMaxConcurrent: 2
    terminalMaxPerUser: 1
    terminalUser: "one-terminal"
    terminalWorkingDirectory: "/var/lib/one-terminal"
bastion:
    enabled: false
    collectSchedule: "*/1 * * * *"
    collectTimeoutSeconds: 15
    maxConcurrentCollects: 5
    retentionDays: 30
    cleanupSchedule: "30 4 * * *"
`

// LoadConfig reads the application configuration without panicking. When no
// path is provided it uses the application data directory.
func LoadConfig(path ...string) (*viper.Viper, error) {
	configPath := filepath.Join(GetBasePath(), "config.yaml")
	if configuredPath := strings.TrimSpace(os.Getenv("ONEINSTACK_CONFIG_PATH")); configuredPath != "" {
		configPath = configuredPath
	}
	if len(path) > 0 && strings.TrimSpace(path[0]) != "" {
		configPath = path[0]
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return nil, fmt.Errorf("create config directory: %w", err)
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte(defaultConfig), 0600); err != nil {
			return nil, fmt.Errorf("create default config: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat config file: %w", err)
	}

	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	v.SetEnvPrefix("ONEINSTACK")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("system.port", "8089")
	v.SetDefault("system.bindAddress", "0.0.0.0")
	v.SetDefault("system.httpsEnabled", false)
	v.SetDefault("system.httpsPort", "8443")
	v.SetDefault("system.httpsCertificateFile", "")
	v.SetDefault("system.httpsPrivateKeyFile", "")
	v.SetDefault("system.trustedProxies", []string{})
	v.SetDefault("system.panelEntryEnabled", false)
	v.SetDefault("system.panelEntryPath", "")
	v.SetDefault("system.fileUploadMaxBytes", int64(100<<20))
	v.SetDefault("system.fileEditMaxBytes", int64(10<<20))
	v.SetDefault("system.fileRootQuotaBytes", int64(0))
	v.SetDefault("system.fileMinFreeBytes", int64(1<<30))
	v.SetDefault("system.trashRetentionDays", 30)
	v.SetDefault("system.trashCleanupSchedule", "0 3 * * *")
	v.SetDefault("system.softwareTaskRetentionDays", 90)
	v.SetDefault("system.softwareTaskLogRetentionDays", 30)
	v.SetDefault("system.softwareTaskCleanupSchedule", "30 3 * * *")
	v.SetDefault("system.databaseBackupRetentionDays", 30)
	v.SetDefault("system.databaseBackupCleanupSchedule", "0 4 * * *")
	v.SetDefault("system.websiteBackupRetentionDays", 30)
	v.SetDefault("system.websiteBackupCleanupSchedule", "15 4 * * *")
	v.SetDefault("system.websiteBackupMaxBytes", int64(20<<30))
	v.SetDefault("system.websiteBackupMaxFiles", 200000)
	v.SetDefault("system.auditRetentionDays", 365)
	v.SetDefault("system.auditCleanupSchedule", "45 4 * * *")
	v.SetDefault("system.auditExportMaxRows", 10000)
	v.SetDefault("system.monitorSampleSchedule", "*/1 * * * *")
	v.SetDefault("system.monitorRetentionDays", 30)
	v.SetDefault("system.monitorAlertRetentionDays", 365)
	v.SetDefault("system.monitorCleanupSchedule", "20 4 * * *")
	v.SetDefault("system.runtimeLogRetentionDays", 30)
	v.SetDefault("system.runtimeLogCleanupSchedule", "10 5 * * *")
	v.SetDefault("system.cronExecutionRetentionDays", 30)
	v.SetDefault("system.cronExecutionCleanupSchedule", "25 5 * * *")
	v.SetDefault("system.certificatePath", filepath.Join(GetBasePath(), "certificates"))
	v.SetDefault("system.acmeChallengePath", filepath.Join(GetBasePath(), "acme-webroot"))
	v.SetDefault("system.acmeDirectoryUrl", "https://acme-v02.api.letsencrypt.org/directory")
	v.SetDefault("system.acmeRenewSchedule", "15 3 * * *")
	v.SetDefault("system.acmeRenewBeforeDays", 30)
	v.SetDefault("system.certificateExpiryWarningDays", 30)
	v.SetDefault("system.acmeIssueTimeoutMinutes", 15)
	v.SetDefault("system.terminalEnabled", false)
	v.SetDefault("system.terminalSessionMinutes", 15)
	v.SetDefault("system.allowInsecureWebSocketInDev", false)
	v.SetDefault("system.allowInlineStyle", false)
	v.SetDefault("system.terminalIdleMinutes", 5)
	v.SetDefault("system.terminalMaxConcurrent", 2)
	v.SetDefault("system.terminalMaxPerUser", 1)
	v.SetDefault("system.terminalUser", "one-terminal")
	v.SetDefault("system.terminalWorkingDirectory", "/var/lib/one-terminal")
	v.SetDefault("scriptCenter.enabled", false)
	v.SetDefault("scriptCenter.allowInsecureHTTP", false)
	v.SetDefault("scriptCenter.channel", "stable")
	v.SetDefault("scriptCenter.requestTimeoutSeconds", 10)
	v.SetDefault("scriptCenter.maxPackageBytes", int64(64<<20))
	v.SetDefault("scriptCenter.maxExpandedBytes", int64(256<<20))
	v.SetDefault("scriptCenter.cachePath", filepath.Join(GetBasePath(), "script-registry", "cache"))
	v.SetDefault("scriptCenter.bundledPath", filepath.Join(GetBasePath(), "script-registry", "bundled"))
	v.SetDefault("scriptCenter.catalogSyncIntervalMinutes", 15)
	v.SetDefault("scriptCenter.catalogStaleAfterHours", 24)
	v.SetDefault("updateCenter.enabled", false)
	v.SetDefault("updateCenter.centerUrl", "")
	v.SetDefault("updateCenter.channel", "stable")
	v.SetDefault("updateCenter.requestTimeoutSeconds", 20)
	v.SetDefault("updateCenter.maxPackageBytes", int64(256<<20))
	v.SetDefault("updateCenter.maxExpandedBytes", int64(512<<20))
	v.SetDefault("updateCenter.healthTimeoutSeconds", 60)
	v.SetDefault("updateCenter.backupRetention", 5)
	v.SetDefault("bastion.enabled", false)
	v.SetDefault("bastion.collectSchedule", "*/1 * * * *")
	v.SetDefault("bastion.collectTimeoutSeconds", 15)
	v.SetDefault("bastion.maxConcurrentCollects", 5)
	v.SetDefault("bastion.retentionDays", 30)
	v.SetDefault("bastion.cleanupSchedule", "30 4 * * *")
	for key, environmentName := range map[string]string{
		"system.port":                             "ONEINSTACK_SYSTEM_PORT",
		"system.bindAddress":                      "ONEINSTACK_SYSTEM_BIND_ADDRESS",
		"system.httpsEnabled":                     "ONEINSTACK_SYSTEM_HTTPS_ENABLED",
		"system.httpsPort":                        "ONEINSTACK_SYSTEM_HTTPS_PORT",
		"system.httpsCertificateFile":             "ONEINSTACK_SYSTEM_HTTPS_CERTIFICATE_FILE",
		"system.httpsPrivateKeyFile":              "ONEINSTACK_SYSTEM_HTTPS_PRIVATE_KEY_FILE",
		"system.panelEntryEnabled":                "ONEINSTACK_SYSTEM_PANEL_ENTRY_ENABLED",
		"system.panelEntryPath":                   "ONEINSTACK_SYSTEM_PANEL_ENTRY_PATH",
		"system.defaultPath":                      "ONEINSTACK_SYSTEM_DEFAULT_PATH",
		"system.allowInsecureWebSocketInDev":      "ONEINSTACK_SYSTEM_ALLOW_INSECURE_WEBSOCKET_IN_DEV",
		"system.allowInlineStyle":                 "ONEINSTACK_SYSTEM_ALLOW_INLINE_STYLE",
		"system.fileUploadMaxBytes":               "ONEINSTACK_SYSTEM_FILE_UPLOAD_MAX_BYTES",
		"system.fileEditMaxBytes":                 "ONEINSTACK_SYSTEM_FILE_EDIT_MAX_BYTES",
		"system.fileRootQuotaBytes":               "ONEINSTACK_SYSTEM_FILE_ROOT_QUOTA_BYTES",
		"system.fileMinFreeBytes":                 "ONEINSTACK_SYSTEM_FILE_MIN_FREE_BYTES",
		"system.trashRetentionDays":               "ONEINSTACK_SYSTEM_TRASH_RETENTION_DAYS",
		"system.trashCleanupSchedule":             "ONEINSTACK_SYSTEM_TRASH_CLEANUP_SCHEDULE",
		"system.softwareTaskRetentionDays":        "ONEINSTACK_SYSTEM_SOFTWARE_TASK_RETENTION_DAYS",
		"system.softwareTaskLogRetentionDays":     "ONEINSTACK_SYSTEM_SOFTWARE_TASK_LOG_RETENTION_DAYS",
		"system.softwareTaskCleanupSchedule":      "ONEINSTACK_SYSTEM_SOFTWARE_TASK_CLEANUP_SCHEDULE",
		"system.databaseBackupRetentionDays":      "ONEINSTACK_SYSTEM_DATABASE_BACKUP_RETENTION_DAYS",
		"system.databaseBackupCleanupSchedule":    "ONEINSTACK_SYSTEM_DATABASE_BACKUP_CLEANUP_SCHEDULE",
		"system.websiteBackupRetentionDays":       "ONEINSTACK_SYSTEM_WEBSITE_BACKUP_RETENTION_DAYS",
		"system.websiteBackupCleanupSchedule":     "ONEINSTACK_SYSTEM_WEBSITE_BACKUP_CLEANUP_SCHEDULE",
		"system.websiteBackupMaxBytes":            "ONEINSTACK_SYSTEM_WEBSITE_BACKUP_MAX_BYTES",
		"system.websiteBackupMaxFiles":            "ONEINSTACK_SYSTEM_WEBSITE_BACKUP_MAX_FILES",
		"system.auditRetentionDays":               "ONEINSTACK_SYSTEM_AUDIT_RETENTION_DAYS",
		"system.auditCleanupSchedule":             "ONEINSTACK_SYSTEM_AUDIT_CLEANUP_SCHEDULE",
		"system.auditExportMaxRows":               "ONEINSTACK_SYSTEM_AUDIT_EXPORT_MAX_ROWS",
		"system.monitorSampleSchedule":            "ONEINSTACK_SYSTEM_MONITOR_SAMPLE_SCHEDULE",
		"system.monitorRetentionDays":             "ONEINSTACK_SYSTEM_MONITOR_RETENTION_DAYS",
		"system.monitorAlertRetentionDays":        "ONEINSTACK_SYSTEM_MONITOR_ALERT_RETENTION_DAYS",
		"system.monitorCleanupSchedule":           "ONEINSTACK_SYSTEM_MONITOR_CLEANUP_SCHEDULE",
		"system.runtimeLogRetentionDays":          "ONEINSTACK_SYSTEM_RUNTIME_LOG_RETENTION_DAYS",
		"system.runtimeLogCleanupSchedule":        "ONEINSTACK_SYSTEM_RUNTIME_LOG_CLEANUP_SCHEDULE",
		"system.cronExecutionRetentionDays":       "ONEINSTACK_SYSTEM_CRON_EXECUTION_RETENTION_DAYS",
		"system.cronExecutionCleanupSchedule":     "ONEINSTACK_SYSTEM_CRON_EXECUTION_CLEANUP_SCHEDULE",
		"system.certificatePath":                  "ONEINSTACK_SYSTEM_CERTIFICATE_PATH",
		"system.acmeChallengePath":                "ONEINSTACK_SYSTEM_ACME_CHALLENGE_PATH",
		"system.acmeDirectoryUrl":                 "ONEINSTACK_SYSTEM_ACME_DIRECTORY_URL",
		"system.acmeRenewSchedule":                "ONEINSTACK_SYSTEM_ACME_RENEW_SCHEDULE",
		"system.acmeRenewBeforeDays":              "ONEINSTACK_SYSTEM_ACME_RENEW_BEFORE_DAYS",
		"system.certificateExpiryWarningDays":     "ONEINSTACK_SYSTEM_CERTIFICATE_EXPIRY_WARNING_DAYS",
		"system.acmeIssueTimeoutMinutes":          "ONEINSTACK_SYSTEM_ACME_ISSUE_TIMEOUT_MINUTES",
		"system.terminalEnabled":                  "ONEINSTACK_SYSTEM_TERMINAL_ENABLED",
		"system.terminalSessionMinutes":           "ONEINSTACK_SYSTEM_TERMINAL_SESSION_MINUTES",
		"system.terminalIdleMinutes":              "ONEINSTACK_SYSTEM_TERMINAL_IDLE_MINUTES",
		"system.terminalMaxConcurrent":            "ONEINSTACK_SYSTEM_TERMINAL_MAX_CONCURRENT",
		"system.terminalMaxPerUser":               "ONEINSTACK_SYSTEM_TERMINAL_MAX_PER_USER",
		"system.terminalUser":                     "ONEINSTACK_SYSTEM_TERMINAL_USER",
		"system.terminalWorkingDirectory":         "ONEINSTACK_SYSTEM_TERMINAL_WORKING_DIRECTORY",
		"scriptCenter.enabled":                    "ONEINSTACK_SCRIPT_CENTER_ENABLED",
		"scriptCenter.allowInsecureHTTP":          "ONEINSTACK_SCRIPT_CENTER_ALLOW_INSECURE_HTTP",
		"scriptCenter.url":                        "ONEINSTACK_SCRIPT_CENTER_URL",
		"scriptCenter.channel":                    "ONEINSTACK_SCRIPT_CENTER_CHANNEL",
		"scriptCenter.requestTimeoutSeconds":      "ONEINSTACK_SCRIPT_CENTER_REQUEST_TIMEOUT_SECONDS",
		"scriptCenter.maxPackageBytes":            "ONEINSTACK_SCRIPT_CENTER_MAX_PACKAGE_BYTES",
		"scriptCenter.maxExpandedBytes":           "ONEINSTACK_SCRIPT_CENTER_MAX_EXPANDED_BYTES",
		"scriptCenter.catalogSyncIntervalMinutes": "ONEINSTACK_SCRIPT_CENTER_CATALOG_SYNC_INTERVAL_MINUTES",
		"scriptCenter.catalogStaleAfterHours":     "ONEINSTACK_SCRIPT_CENTER_CATALOG_STALE_AFTER_HOURS",
		"scriptCenter.cachePath":                  "ONEINSTACK_SCRIPT_CENTER_CACHE_PATH",
		"scriptCenter.bundledPath":                "ONEINSTACK_SCRIPT_CENTER_BUNDLED_PATH",
		"updateCenter.enabled":                    "ONEINSTACK_UPDATE_CENTER_ENABLED",
		"updateCenter.centerUrl":                  "ONEINSTACK_UPDATE_CENTER_URL",
		"updateCenter.manifestUrl":                "ONEINSTACK_UPDATE_CENTER_MANIFEST_URL",
		"updateCenter.channel":                    "ONEINSTACK_UPDATE_CENTER_CHANNEL",
		"updateCenter.requestTimeoutSeconds":      "ONEINSTACK_UPDATE_CENTER_REQUEST_TIMEOUT_SECONDS",
		"updateCenter.maxPackageBytes":            "ONEINSTACK_UPDATE_CENTER_MAX_PACKAGE_BYTES",
		"updateCenter.maxExpandedBytes":           "ONEINSTACK_UPDATE_CENTER_MAX_EXPANDED_BYTES",
		"updateCenter.healthTimeoutSeconds":       "ONEINSTACK_UPDATE_CENTER_HEALTH_TIMEOUT_SECONDS",
		"updateCenter.backupRetention":            "ONEINSTACK_UPDATE_CENTER_BACKUP_RETENTION",
		"bastion.enabled":                         "ONEINSTACK_BASTION_ENABLED",
		"bastion.collectSchedule":                 "ONEINSTACK_BASTION_COLLECT_SCHEDULE",
		"bastion.collectTimeoutSeconds":           "ONEINSTACK_BASTION_COLLECT_TIMEOUT_SECONDS",
		"bastion.maxConcurrentCollects":           "ONEINSTACK_BASTION_MAX_CONCURRENT_COLLECTS",
		"bastion.retentionDays":                   "ONEINSTACK_BASTION_RETENTION_DAYS",
		"bastion.cleanupSchedule":                 "ONEINSTACK_BASTION_CLEANUP_SCHEDULE",
	} {
		if err := v.BindEnv(key, environmentName); err != nil {
			return nil, fmt.Errorf("bind environment %s: %w", environmentName, err)
		}
	}

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	if encoded := strings.TrimSpace(os.Getenv("ONEINSTACK_SCRIPT_CENTER_TRUSTED_KEYS")); encoded != "" {
		keys := map[string]string{}
		if err := json.Unmarshal([]byte(encoded), &keys); err != nil {
			return nil, fmt.Errorf("decode ONEINSTACK_SCRIPT_CENTER_TRUSTED_KEYS: %w", err)
		}
		v.Set("scriptCenter.trustedKeys", keys)
	}
	if err := initializeJWTSecret(v, configPath); err != nil {
		return nil, err
	}
	if err := initializeCredentialKey(v, configPath); err != nil {
		return nil, err
	}

	if err := v.Unmarshal(&ONE_CONFIG); err != nil {
		return nil, fmt.Errorf("decode config file: %w", err)
	}
	if err := validateSystemConfig(); err != nil {
		return nil, err
	}
	if err := validateScriptCenterConfig(); err != nil {
		return nil, err
	}
	if err := validateUpdateCenterConfig(); err != nil {
		return nil, err
	}

	ONE_VIP = v
	return v, nil
}

func validateUpdateCenterConfig() error {
	center := ONE_CONFIG.UpdateCenter
	switch center.Channel {
	case "stable", "beta", "development":
	default:
		return fmt.Errorf("validate config: updateCenter.channel must be stable, beta, or development")
	}
	if center.RequestTimeoutSeconds < 1 || center.RequestTimeoutSeconds > 300 {
		return fmt.Errorf("validate config: updateCenter.requestTimeoutSeconds must be between 1 and 300")
	}
	if center.MaxPackageBytes < 1<<20 || center.MaxPackageBytes > 2<<30 {
		return fmt.Errorf("validate config: updateCenter.maxPackageBytes must be between 1 MiB and 2 GiB")
	}
	if center.MaxExpandedBytes < center.MaxPackageBytes || center.MaxExpandedBytes > 4<<30 {
		return fmt.Errorf("validate config: updateCenter.maxExpandedBytes must be between maxPackageBytes and 4 GiB")
	}
	if center.HealthTimeoutSeconds < 10 || center.HealthTimeoutSeconds > 600 {
		return fmt.Errorf("validate config: updateCenter.healthTimeoutSeconds must be between 10 and 600")
	}
	if center.BackupRetention < 1 || center.BackupRetention > 50 {
		return fmt.Errorf("validate config: updateCenter.backupRetention must be between 1 and 50")
	}
	if center.Enabled {
		updateURL := strings.TrimSpace(center.CenterURL)
		if updateURL == "" && ONE_CONFIG.ScriptCenter.Enabled {
			updateURL = strings.TrimSpace(ONE_CONFIG.ScriptCenter.URL)
		}
		if updateURL == "" {
			updateURL = strings.TrimSpace(center.ManifestURL)
		}
		parsed, err := url.Parse(updateURL)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname()))) {
			return fmt.Errorf("validate config: enabled updateCenter.centerUrl/manifestUrl must use HTTPS (HTTP is allowed only for loopback development)")
		}
		trustedKeys := center.TrustedKeys
		if len(trustedKeys) == 0 {
			trustedKeys = ONE_CONFIG.ScriptCenter.TrustedKeys
		}
		if len(trustedKeys) == 0 {
			return fmt.Errorf("validate config: enabled updateCenter requires at least one trusted key")
		}
		for keyID, encoded := range trustedKeys {
			if strings.TrimSpace(keyID) == "" {
				return fmt.Errorf("validate config: updateCenter trusted key ID must not be empty")
			}
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
			if err != nil || len(decoded) != ed25519.PublicKeySize {
				return fmt.Errorf("validate config: updateCenter trusted key %q must be a base64 Ed25519 public key", keyID)
			}
		}
	}
	return nil
}

func validateScriptCenterConfig() error {
	center := ONE_CONFIG.ScriptCenter
	switch center.Channel {
	case "stable", "beta", "development":
	default:
		return fmt.Errorf("validate config: scriptCenter.channel must be stable, beta, or development")
	}
	if center.RequestTimeoutSeconds < 1 || center.RequestTimeoutSeconds > 120 {
		return fmt.Errorf("validate config: scriptCenter.requestTimeoutSeconds must be between 1 and 120")
	}
	if center.MaxPackageBytes < 1<<20 || center.MaxPackageBytes > 1<<30 {
		return fmt.Errorf("validate config: scriptCenter.maxPackageBytes must be between 1 MiB and 1 GiB")
	}
	if center.MaxExpandedBytes < center.MaxPackageBytes || center.MaxExpandedBytes > 4<<30 {
		return fmt.Errorf("validate config: scriptCenter.maxExpandedBytes must be between maxPackageBytes and 4 GiB")
	}
	if strings.TrimSpace(center.CachePath) == "" || strings.TrimSpace(center.BundledPath) == "" {
		return fmt.Errorf("validate config: scriptCenter cachePath and bundledPath cannot be empty")
	}
	if center.CatalogSyncIntervalMinutes < 1 || center.CatalogSyncIntervalMinutes > 1440 {
		return fmt.Errorf("validate config: scriptCenter.catalogSyncIntervalMinutes must be between 1 and 1440")
	}
	if center.CatalogStaleAfterHours < 1 || center.CatalogStaleAfterHours > 8760 {
		return fmt.Errorf("validate config: scriptCenter.catalogStaleAfterHours must be between 1 and 8760")
	}
	if center.Enabled {
		parsed, err := url.Parse(center.URL)
		httpAllowed := parsed != nil && parsed.Scheme == "http" &&
			(isLoopbackHost(parsed.Hostname()) || center.AllowInsecureHTTP)
		if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !httpAllowed) {
			return fmt.Errorf("validate config: enabled scriptCenter.url must use HTTPS (HTTP requires loopback or explicit development opt-in)")
		}
		if len(center.TrustedKeys) == 0 {
			return fmt.Errorf("validate config: enabled scriptCenter requires at least one trusted key")
		}
		for keyID, encoded := range center.TrustedKeys {
			decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
			if strings.TrimSpace(keyID) == "" || decodeErr != nil || len(decoded) != ed25519.PublicKeySize {
				return fmt.Errorf("validate config: scriptCenter trusted key %q must be a base64 Ed25519 public key", keyID)
			}
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func initializeJWTSecret(v *viper.Viper, configPath string) error {
	secret := strings.TrimSpace(v.GetString("system.jwtSecret"))
	if secret == "" || secret == legacyInsecureJWTSecret {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return fmt.Errorf("generate JWT secret: %w", err)
		}
		secret = hex.EncodeToString(random)
		v.Set("system.jwtSecret", secret)
		if err := v.WriteConfig(); err != nil {
			return fmt.Errorf("persist JWT secret: %w", err)
		}
		if err := os.Chmod(configPath, 0600); err != nil {
			return fmt.Errorf("secure config permissions: %w", err)
		}
	}

	key := []byte(secret)
	if decoded, err := hex.DecodeString(secret); err == nil && len(decoded) >= 32 {
		key = decoded
	}
	if err := utils.ConfigureJWTKey(key); err != nil {
		return fmt.Errorf("configure JWT secret: %w", err)
	}
	return nil
}

func initializeCredentialKey(v *viper.Viper, configPath string) error {
	secret := strings.TrimSpace(v.GetString("system.credentialKey"))
	if secret == "" {
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return fmt.Errorf("generate credential encryption key: %w", err)
		}
		secret = hex.EncodeToString(random)
		v.Set("system.credentialKey", secret)
		if err := v.WriteConfig(); err != nil {
			return fmt.Errorf("persist credential encryption key: %w", err)
		}
		if err := os.Chmod(configPath, 0600); err != nil {
			return fmt.Errorf("secure config permissions: %w", err)
		}
	}
	key, err := hex.DecodeString(secret)
	if err != nil || len(key) != 32 {
		return fmt.Errorf("validate config: system.credentialKey must be a 32-byte hex key")
	}
	if err := utils.ConfigureCredentialKey(key); err != nil {
		return fmt.Errorf("configure credential encryption key: %w", err)
	}
	return nil
}

func validateSystemConfig() error {
	system := ONE_CONFIG.System
	if err := validatePanelNetworkConfig(
		system.BindAddress,
		system.Port,
		system.HTTPSEnabled,
		system.HTTPSPort,
		system.HTTPSCertificateFile,
		system.HTTPSPrivateKeyFile,
		system.TrustedProxies,
	); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if strings.TrimSpace(system.DefaultPath) == "" {
		return fmt.Errorf("validate config: system.defaultPath cannot be empty")
	}
	if system.FileUploadMaxBytes <= 0 {
		return fmt.Errorf("validate config: system.fileUploadMaxBytes must be greater than zero")
	}
	if system.FileEditMaxBytes <= 0 || system.FileEditMaxBytes > system.FileUploadMaxBytes {
		return fmt.Errorf("validate config: system.fileEditMaxBytes must be positive and no greater than fileUploadMaxBytes")
	}
	if system.FileRootQuotaBytes < 0 || system.FileMinFreeBytes < 0 {
		return fmt.Errorf("validate config: file quota and minimum free bytes cannot be negative")
	}
	if system.FileRootQuotaBytes > 0 && system.FileRootQuotaBytes < system.FileUploadMaxBytes {
		return fmt.Errorf("validate config: system.fileRootQuotaBytes must be zero or at least fileUploadMaxBytes")
	}
	if system.TrashRetentionDays < 1 || system.TrashRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.trashRetentionDays must be between 1 and 3650")
	}
	if strings.TrimSpace(system.TrashCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.trashCleanupSchedule cannot be empty")
	}
	if system.SoftwareTaskRetentionDays < 1 || system.SoftwareTaskRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.softwareTaskRetentionDays must be between 1 and 3650")
	}
	if system.SoftwareTaskLogRetentionDays < 1 || system.SoftwareTaskLogRetentionDays > system.SoftwareTaskRetentionDays {
		return fmt.Errorf("validate config: system.softwareTaskLogRetentionDays must be between 1 and softwareTaskRetentionDays")
	}
	if strings.TrimSpace(system.SoftwareTaskCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.softwareTaskCleanupSchedule cannot be empty")
	}
	if system.DatabaseBackupRetentionDays < 1 || system.DatabaseBackupRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.databaseBackupRetentionDays must be between 1 and 3650")
	}
	if strings.TrimSpace(system.DatabaseBackupCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.databaseBackupCleanupSchedule cannot be empty")
	}
	if system.WebsiteBackupRetentionDays < 1 || system.WebsiteBackupRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.websiteBackupRetentionDays must be between 1 and 3650")
	}
	if strings.TrimSpace(system.WebsiteBackupCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.websiteBackupCleanupSchedule cannot be empty")
	}
	if system.WebsiteBackupMaxBytes < 1<<20 || system.WebsiteBackupMaxBytes > 1<<40 {
		return fmt.Errorf("validate config: system.websiteBackupMaxBytes must be between 1 MiB and 1 TiB")
	}
	if system.WebsiteBackupMaxFiles < 1 || system.WebsiteBackupMaxFiles > 1000000 {
		return fmt.Errorf("validate config: system.websiteBackupMaxFiles must be between 1 and 1000000")
	}
	if system.AuditRetentionDays < 30 || system.AuditRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.auditRetentionDays must be between 30 and 3650")
	}
	if strings.TrimSpace(system.AuditCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.auditCleanupSchedule cannot be empty")
	}
	if system.AuditExportMaxRows < 100 || system.AuditExportMaxRows > 100000 {
		return fmt.Errorf("validate config: system.auditExportMaxRows must be between 100 and 100000")
	}
	if strings.TrimSpace(system.MonitorSampleSchedule) == "" ||
		strings.TrimSpace(system.MonitorCleanupSchedule) == "" {
		return fmt.Errorf("validate config: monitor schedules cannot be empty")
	}
	if system.MonitorRetentionDays < 1 || system.MonitorRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.monitorRetentionDays must be between 1 and 3650")
	}
	if system.MonitorAlertRetentionDays < 1 || system.MonitorAlertRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.monitorAlertRetentionDays must be between 1 and 3650")
	}
	if system.RuntimeLogRetentionDays < 1 || system.RuntimeLogRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.runtimeLogRetentionDays must be between 1 and 3650")
	}
	if strings.TrimSpace(system.RuntimeLogCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.runtimeLogCleanupSchedule cannot be empty")
	}
	if system.CronExecutionRetentionDays < 1 || system.CronExecutionRetentionDays > 3650 {
		return fmt.Errorf("validate config: system.cronExecutionRetentionDays must be between 1 and 3650")
	}
	if strings.TrimSpace(system.CronExecutionCleanupSchedule) == "" {
		return fmt.Errorf("validate config: system.cronExecutionCleanupSchedule cannot be empty")
	}
	for label, configuredPath := range map[string]string{
		"system.certificatePath":   system.CertificatePath,
		"system.acmeChallengePath": system.ACMEChallengePath,
	} {
		cleaned := filepath.Clean(strings.TrimSpace(configuredPath))
		if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
			return fmt.Errorf("validate config: %s must be a non-root absolute path", label)
		}
	}
	directoryURL, err := url.Parse(strings.TrimSpace(system.ACMEDirectoryURL))
	if err != nil || directoryURL.Host == "" ||
		(directoryURL.Scheme != "https" &&
			!(directoryURL.Scheme == "http" && isLoopbackHost(directoryURL.Hostname()))) {
		return fmt.Errorf("validate config: system.acmeDirectoryUrl must use HTTPS (HTTP is allowed only for loopback tests)")
	}
	if strings.TrimSpace(system.ACMERenewSchedule) == "" {
		return fmt.Errorf("validate config: system.acmeRenewSchedule cannot be empty")
	}
	if system.ACMERenewBeforeDays < 1 || system.ACMERenewBeforeDays > 90 {
		return fmt.Errorf("validate config: system.acmeRenewBeforeDays must be between 1 and 90")
	}
	if system.CertificateExpiryWarningDays < 1 || system.CertificateExpiryWarningDays > 180 {
		return fmt.Errorf("validate config: system.certificateExpiryWarningDays must be between 1 and 180")
	}
	if system.ACMEIssueTimeoutMinutes < 1 || system.ACMEIssueTimeoutMinutes > 120 {
		return fmt.Errorf("validate config: system.acmeIssueTimeoutMinutes must be between 1 and 120")
	}
	if system.TerminalSessionMins < 1 || system.TerminalSessionMins > 120 {
		return fmt.Errorf("validate config: system.terminalSessionMinutes must be between 1 and 120")
	}
	if system.TerminalIdleMins < 1 || system.TerminalIdleMins > system.TerminalSessionMins {
		return fmt.Errorf("validate config: system.terminalIdleMinutes must be between 1 and terminalSessionMinutes")
	}
	if system.TerminalMaxConcurrent < 1 || system.TerminalMaxConcurrent > 10 {
		return fmt.Errorf("validate config: system.terminalMaxConcurrent must be between 1 and 10")
	}
	if system.TerminalMaxPerUser < 1 ||
		system.TerminalMaxPerUser > system.TerminalMaxConcurrent {
		return fmt.Errorf("validate config: system.terminalMaxPerUser must be between 1 and terminalMaxConcurrent")
	}
	if !validTerminalUsername(system.TerminalUser) || system.TerminalUser == "root" {
		return fmt.Errorf("validate config: system.terminalUser must be a non-root Linux username")
	}
	terminalWorkingDirectory := filepath.Clean(strings.TrimSpace(system.TerminalWorkingDirectory))
	if !filepath.IsAbs(terminalWorkingDirectory) || terminalWorkingDirectory == "/" {
		return fmt.Errorf("validate config: system.terminalWorkingDirectory must be an absolute non-root path")
	}
	return nil
}

func validTerminalUsername(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 32 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' && index > 0 ||
			(character == '-' || character == '_') && index > 0 {
			continue
		}
		return false
	}
	return true
}

func validatePanelNetworkConfig(
	bindAddress, httpPort string,
	httpsEnabled bool,
	httpsPort, certificateFile, privateKeyFile string,
	trustedProxies []string,
) error {
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress != "" && net.ParseIP(bindAddress) == nil {
		return fmt.Errorf("system.bindAddress must be an IPv4 or IPv6 address")
	}
	validatePort := func(label, value string) error {
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%s must be an integer between 1 and 65535", label)
		}
		return nil
	}
	if err := validatePort("system.port", httpPort); err != nil {
		return err
	}
	if httpsEnabled {
		if err := validatePort("system.httpsPort", httpsPort); err != nil {
			return err
		}
		if strings.TrimSpace(httpPort) == strings.TrimSpace(httpsPort) {
			return fmt.Errorf("system.port and system.httpsPort must be different")
		}
		if strings.TrimSpace(certificateFile) == "" || strings.TrimSpace(privateKeyFile) == "" {
			return fmt.Errorf("enabled HTTPS requires certificate and private key files")
		}
	}
	for _, trustedProxy := range trustedProxies {
		trustedProxy = strings.TrimSpace(trustedProxy)
		if trustedProxy == "" {
			return fmt.Errorf("system.trustedProxies must not contain empty entries")
		}
		if net.ParseIP(trustedProxy) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(trustedProxy); err != nil {
			return fmt.Errorf("trusted proxy %q must be an IP address or CIDR range", trustedProxy)
		}
	}
	return nil
}

// Viper is kept for compatibility with older callers. New code should use
// LoadConfig and handle the returned error.
func Viper(path ...string) *viper.Viper {
	v, err := LoadConfig(path...)
	if err != nil {
		panic(err)
	}
	return v
}
