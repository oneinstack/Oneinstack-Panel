package panelbackup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"oneinstack/app"
	"oneinstack/internal/services/website"
)

// ListApplicationBackupsReadOnly inspects existing backup metadata without
// creating the backup directory when the feature has not been used yet.
func ListApplicationBackupsReadOnly() ([]BackupInfo, error) {
	root := strings.TrimSpace(os.Getenv("ONEINSTACK_PANEL_BACKUP_DIR"))
	if root == "" {
		root = filepath.Join(filepath.Clean(app.GetBasePath()), "backups", "panel")
	}
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return nil, ErrInvalidBackup
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalidBackup
	}
	return (&Manager{config: Config{BackupRoot: root}}).List()
}

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
		WebServerDetector: func() (string, error) {
			server, err := website.DetectWebServer()
			if err != nil {
				return "", err
			}
			return server.Component, nil
		},
	}, database)
}
