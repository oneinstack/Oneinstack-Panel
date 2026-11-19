package legacyoneinstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

const MigrationUnavailableNotice = "Panel 暂不支持将脚本接管的组件迁移为受管组件。升级、卸载和模板配置不可用。"

type MigrationUnavailableError struct {
	Component string
}

func (MigrationUnavailableError) ErrorCode() string { return "EXTERNAL_MIGRATION_REQUIRED" }

func (e MigrationUnavailableError) Error() string {
	return e.ErrorCode() + ": " + e.Notice()
}

func (e MigrationUnavailableError) Notice() string {
	if e.Component == "redis" {
		return "请先在软件商店完成接管 Redis 的显式迁移。"
	}
	return MigrationUnavailableNotice
}

// Import only writes Panel metadata. Existing programs, units, configurations,
// certificates, cron jobs and data directories are never moved or rewritten.
func Import(ctx context.Context, db *gorm.DB, report *Report, taskID string) error {
	if db == nil || report == nil {
		return errors.New("legacy import is not initialized")
	}
	report.ImportedComponents, report.ImportedSites = 0, 0
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "sqlite" {
			// Reserve SQLite's writer before reading the import snapshot.
			// Startup reporters can otherwise commit between the reads and
			// first insert, making WAL snapshot promotion fail with BUSY.
			// UpdateColumn skips hooks/timestamps and leaves the task intact.
			if err := tx.Model(&models.SoftwareTask{}).Where("id = ?", taskID).UpdateColumn("id", gorm.Expr("id")).Error; err != nil {
				return err
			}
		}
		acceptedWeb := map[string]bool{}
		var busy int64
		if err := tx.Model(&models.SoftwareTask{}).Where("id <> ? AND status IN ?", taskID, models.ActiveSoftwareTaskStatuses()).Count(&busy).Error; err != nil {
			return err
		}
		if busy > 0 {
			return errors.New("COMPONENT_BUSY: wait for active component tasks before importing")
		}
		if tx.Migrator().HasTable(&models.WebsiteTask{}) {
			if err := tx.Model(&models.WebsiteTask{}).Where("status IN ?", []string{"queued", "running", "canceling"}).Count(&busy).Error; err != nil {
				return err
			}
			if busy > 0 {
				return errors.New("COMPONENT_BUSY: wait for active website tasks before importing")
			}
		}
		for _, c := range report.Components {
			var existing models.Software
			err := tx.Where("installed = ? AND (`key` = ? OR component = ?)", true, c.Key, c.ID).Order("id DESC").First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil && existing.Origin != Origin {
				report.Warnings = append(report.Warnings, "MANAGED_COMPONENT_SKIPPED: "+c.ID+" is already managed by Panel")
				continue
			}
			metadata, _ := json.Marshal(c)
			scopes, _ := json.Marshal(c.Scopes)
			params, _ := json.Marshal(map[string]string{"install-dir": c.InstallDir, "data-dir": c.DataDir, "port": c.Port, "web-root": c.WebRoot, "log-dir": c.LogRoot})
			if errors.Is(err, gorm.ErrRecordNotFound) {
				existing = models.Software{Key: c.Key, Component: c.ID, Name: c.Name, Version: c.Version, Type: "runtime", Origin: Origin, CatalogVisible: true, Installable: false, InstallTime: time.Now(), CreateTime: time.Now()}
			}
			existing.Installed = true
			existing.Status = models.Soft_Status_Suc
			existing.InstallVersion = c.Version
			existing.RuntimeVersion = c.Version
			existing.LegacyMetadata = string(metadata)
			existing.RuntimeParamsJSON = string(params)
			existing.ServiceName = c.Service
			existing.RuntimeGroup = c.RuntimeGroup
			existing.ManageScopesJSON = string(scopes)
			existing.IsUpdate = false
			existing.HttpPort = c.Port
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			// GORM applies the catalog's default:true when creating a bool
			// field. Script resources must never become install targets.
			if err := tx.Model(&existing).UpdateColumn("installable", false).Error; err != nil {
				return err
			}
			acceptedWeb[c.ID] = c.VhostDir != ""
			report.ImportedComponents++
		}
		for _, s := range report.Sites {
			if !acceptedWeb[s.Engine] {
				continue
			}
			var existing models.Website
			err := tx.Where("name = ?", s.Name).First(&existing).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if err == nil {
				if existing.Origin != Origin || !existing.Enabled {
					continue
				}
				old, decodeErr := DecodeSite(existing.LegacyMetadata)
				if decodeErr != nil || old.Config != s.Config {
					continue
				}
			}
			metadata, _ := json.Marshal(s)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				existing = models.Website{Name: s.Name, Engine: s.Engine, Enabled: true, Origin: Origin, Remark: "Imported from OneinStack script"}
			}
			existing.Domain = s.Domains
			existing.RootDir = s.Root
			existing.Dir = s.Root
			existing.Type = s.Type
			existing.LegacyMetadata = string(metadata)
			existing.SendUrl = s.Proxy
			if err := tx.Save(&existing).Error; err != nil {
				return err
			}
			report.ImportedSites++
		}
		return nil
	})
}

// ImportConnections never reads or tests existing database credentials.
// Users connect adopted databases explicitly through the normal connection UI.
func ImportConnections(ctx context.Context, db *gorm.DB, report *Report) []string {
	warnings := []string{}
	if report == nil {
		return warnings
	}
	for _, c := range report.Components {
		switch c.ID {
		case "mysql", "mariadb", "percona", "postgresql", "mongodb", "redis":
			warnings = append(warnings, "DATABASE_CONNECTION_REQUIRED: "+c.Name+" 已接管；请使用原有账号和密码在数据库页面添加连接。")
		}
	}
	return warnings
}

func regexpPort(value string) bool {
	var p int
	n, err := fmt.Sscanf(value, "%d", &p)
	return err == nil && n == 1 && p > 0 && p < 65536 && fmt.Sprint(p) == value
}

func Installed(db *gorm.DB, value string) (*models.Software, error) {
	if db == nil {
		return nil, nil
	}
	var row models.Software
	err := db.Where("installed = ? AND origin = ? AND (`key` = ? OR component = ?)", true, Origin, value, value).Order("install_time DESC, id DESC").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func ProtectMutation(db *gorm.DB, value string) error {
	row, err := Installed(db, value)
	if err != nil {
		return err
	}
	if row != nil {
		return MigrationUnavailableError{Component: row.Component}
	}
	return nil
}
