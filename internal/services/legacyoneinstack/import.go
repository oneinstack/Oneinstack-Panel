package legacyoneinstack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/storage"
	"oneinstack/router/input"
	"oneinstack/utils"

	"gorm.io/gorm"
)

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

// ImportConnections verifies the saved credential against the existing local
// instance and uses the normal encrypted connection store. It never changes a
// database password or creates database users. Failure is reported separately
// from successful environment discovery, without credential-bearing errors.
func ImportConnections(ctx context.Context, db *gorm.DB, report *Report) []string {
	warnings := []string{}
	if report == nil {
		return warnings
	}
	for _, c := range report.Components {
		if c.ID == "redis" {
			data, err := ReadFile(c.Config)
			if err != nil {
				continue
			}
			password, port := "", "6379"
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				if fields[0] == "port" && regexpPort(fields[1]) {
					port = fields[1]
				}
				if fields[0] == "requirepass" {
					if value, ok := staticValue(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "requirepass"))); ok {
						password = value
					}
				}
			}
			request := &input.AddParam{Addr: "127.0.0.1", Port: port, Password: password, Type: "redis", Remark: "OneinStack existing Redis"}
			if err := storage.AddContext(ctx, request); err != nil && !errors.Is(err, storage.ErrConnectionAlreadyExists) {
				warnings = append(warnings, "REDIS_CREDENTIAL_UNVERIFIED: existing Redis is unchanged; connection could not be verified")
			}
			continue
		}
		if c.ID != "mysql" && c.ID != "mariadb" && c.ID != "percona" {
			continue
		}
		if report.options["dbrootpwd"] == "" {
			warnings = append(warnings, "DATABASE_CREDENTIAL_MISSING: existing database credential was not available from a trusted options.conf")
			continue
		}
		port := c.Port
		if port == "" {
			port = "3306"
		}
		request := &input.AddParam{Addr: "127.0.0.1", Port: port, Root: "root", Password: report.options["dbrootpwd"], Type: "mysql", Remark: "OneinStack existing " + c.Name}
		err := storage.AddContext(ctx, request)
		if err != nil && !errors.Is(err, storage.ErrConnectionAlreadyExists) {
			warnings = append(warnings, "DATABASE_CREDENTIAL_UNVERIFIED: existing database remains unchanged; connection could not be verified")
			continue
		}
		var saved models.Storage
		if err := db.Where("addr = ? AND port = ? AND type = ?", request.Addr, request.Port, "mysql").First(&saved).Error; err != nil {
			continue
		}
		// Reuse the normal metadata synchronization so existing databases
		// appear immediately. This does not create users or change grants.
		if err := syncExistingMySQL(ctx, saved); err != nil {
			warnings = append(warnings, "DATABASE_SYNC_INCOMPLETE: connection was preserved; database metadata synchronization needs retry")
		}
	}
	return warnings
}

func syncExistingMySQL(ctx context.Context, saved models.Storage) error {
	password, err := utils.DecryptCredential(saved.Password, utils.CredentialPurposeStoragePassword)
	if err != nil {
		return err
	}
	saved.Password = password
	op := storage.NewMysqlOP(&saved, "information_schema")
	if err := op.ConnectContext(ctx); err != nil {
		return err
	}
	defer op.Close()
	op.DB = op.DB.WithContext(ctx)
	return op.Sync()
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
		return errors.New("EXTERNAL_MIGRATION_REQUIRED: 已接管原有 OneinStack 环境；安装、升级、卸载和模板配置需要先完成对应组件的显式迁移，原有服务和配置保持可用")
	}
	return nil
}
