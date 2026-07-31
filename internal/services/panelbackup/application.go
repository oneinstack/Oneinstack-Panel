package panelbackup

import (
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"oneinstack/app"
)

func NewApplicationManager(database *gorm.DB) (*Manager, error) {
	basePath := filepath.Clean(app.GetBasePath())
	configPath := filepath.Join(basePath, "config.yaml")
	if configured := strings.TrimSpace(os.Getenv("ONEINSTACK_CONFIG_PATH")); configured != "" {
		configPath = configured
	} else if app.ONE_VIP != nil && strings.TrimSpace(app.ONE_VIP.ConfigFileUsed()) != "" {
		configPath = app.ONE_VIP.ConfigFileUsed()
	}
	backupRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_PANEL_BACKUP_DIR"))
	if backupRoot == "" {
		backupRoot = filepath.Join(basePath, "backups", "panel")
	}
	certificatePath := filepath.Clean(app.ONE_CONFIG.System.CertificatePath)
	if certificatePath == "." {
		certificatePath = filepath.Join(basePath, "certificates")
	}
	return NewManager(Config{
		BasePath: basePath, ConfigPath: configPath,
		DatabasePath:    filepath.Join(basePath, "myadmin.db"),
		CertificatePath: certificatePath,
		BackupRoot:      backupRoot,
	}, database)
}
