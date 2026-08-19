package website

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/internal/models"
)

// WebsiteRuntimePreview is the effective virtual-host change that will be
// applied by a website operation. It deliberately contains only managed
// configuration content; callers decide whether to expose the diff.
type WebsiteRuntimePreview struct {
	Website        models.Website
	BeforePath     string
	AfterPath      string
	BeforeContent  string
	AfterContent   string
	CurrentVersion string
	Reload         bool
}

// PreviewCreate renders the normalized website create candidate without
// creating directories, firewall rules, database rows, or Nginx files.
func (service *Service) PreviewCreate(param *models.Website) (WebsiteRuntimePreview, error) {
	// The operation preview receives the already normalized payload produced by
	// PrepareCreate; allow the same managed absolute path during this second
	// render while retaining the no-symlink managed-root validation.
	prepared, err := service.prepareCreate(param, true, true)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	var duplicate int64
	if err := service.DB.Model(&models.Website{}).
		Where("name = ? OR domain = ?", prepared.model.Name, prepared.model.Domain).
		Count(&duplicate).Error; err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if duplicate > 0 {
		return WebsiteRuntimePreview{}, fmt.Errorf("网站 %s 已存在", prepared.model.Name)
	}
	path, err := service.ConfigFile(&prepared.model)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if err := service.validateRuntimeCandidate(path, prepared.config); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	return WebsiteRuntimePreview{
		Website: prepared.model, AfterPath: path, AfterContent: prepared.config,
		Reload: true,
	}, nil
}

// RuntimeRevision hashes the database state and the current managed vhost.
// It is used as the optimistic concurrency token for operation previews.
func (service *Service) RuntimeRevision(id int64) (string, error) {
	if err := service.validate(); err != nil {
		return "", err
	}
	site, err := service.Get(id)
	if err != nil {
		return "", err
	}
	settings, record, err := service.loadSettings(id)
	if err != nil {
		return "", err
	}
	config := ""
	if path, pathErr := service.ConfigFile(site); pathErr == nil {
		if data, readErr := readBoundedFile(path, maxWebServerConfigBytes); readErr == nil {
			config = string(data)
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr
		}
	}
	value, err := json.Marshal(struct {
		Site     models.Website
		Settings WebsiteSettings
		Record   *models.WebsiteSetting
		Config   string
	}{Site: *site, Settings: settings, Record: record, Config: config})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:]), nil
}

// PreviewWebsiteUpdate renders a candidate website model without changing
// the database, filesystem, firewall, or Nginx service.
func (service *Service) PreviewWebsiteUpdate(param *models.Website) (WebsiteRuntimePreview, error) {
	if err := service.validate(); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if param == nil || param.ID <= 0 {
		return WebsiteRuntimePreview{}, errors.New("website ID is required")
	}
	if err := validateWebsiteRootInput(service.WebRoot, param.RootDir, param.Dir, true); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	var existing models.Website
	if err := service.DB.First(&existing, "id = ?", param.ID).Error; err != nil {
		return WebsiteRuntimePreview{}, err
	}
	_, settings, err := service.loadSettings(existing.ID)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	previousTLS, err := service.activeTLSOptions(existing.ID, existing.Domain)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	previous, err := prepareWebsiteWithTLSAndSettings(&existing, service.WebRoot, service.LogRoot, service.challengeRoot(), previousTLS, settings)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(param, service.WebRoot, service.LogRoot, service.challengeRoot(), TLSOptions{}, settings)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	prepared.model.ID = existing.ID
	prepared.model.CreateTime = existing.CreateTime
	prepared.model.Enabled = existing.Enabled
	prepared.model.DisabledReason = existing.DisabledReason
	if err := normalizeWebsiteExpiration(&prepared.model, nowForWebsitePreview()); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	tlsOptions, err := service.activeTLSOptions(existing.ID, prepared.model.Domain)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if tlsOptions.Enabled {
		prepared, err = prepareWebsiteWithTLSAndSettings(param, service.WebRoot, service.LogRoot, service.challengeRoot(), tlsOptions, settings)
		if err != nil {
			return WebsiteRuntimePreview{}, err
		}
		prepared.model.ID = existing.ID
		prepared.model.CreateTime = existing.CreateTime
		prepared.model.Enabled = existing.Enabled
		prepared.model.DisabledReason = existing.DisabledReason
	}
	var duplicate int64
	if err := service.DB.Model(&models.Website{}).
		Where("id <> ? AND (name = ? OR domain = ?)", existing.ID, prepared.model.Name, prepared.model.Domain).
		Count(&duplicate).Error; err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if duplicate > 0 {
		return WebsiteRuntimePreview{}, fmt.Errorf("网站 %s 已存在", prepared.model.Name)
	}
	oldPath, err := service.ConfigFile(&existing)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	newPath, err := service.ConfigFile(&prepared.model)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	before := ""
	if data, readErr := readBoundedFile(oldPath, maxWebServerConfigBytes); readErr == nil {
		before = string(data)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WebsiteRuntimePreview{}, readErr
	}
	after := prepared.config
	if existing.Enabled {
		if before != "" {
			after = mergeWebsiteSettingsConfig(previous.config, prepared.config, before)
		}
	} else {
		after = ""
	}
	if existing.Enabled {
		if err := service.validateRuntimeCandidate(newPath, after); err != nil {
			return WebsiteRuntimePreview{}, err
		}
	}
	version, err := service.RuntimeRevision(existing.ID)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	return WebsiteRuntimePreview{
		Website: existing, BeforePath: oldPath, AfterPath: newPath,
		BeforeContent: before, AfterContent: after, CurrentVersion: version,
		Reload: existing.Enabled,
	}, nil
}

// PreviewSettingsUpdate renders structured settings, including rewrite rules,
// using the same merge behavior as UpdateSettings.
func (service *Service) PreviewSettingsUpdate(id int64, settings WebsiteSettings) (WebsiteRuntimePreview, error) {
	if err := service.validate(); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	site, err := service.Get(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	_, previousRecord, err := service.loadSettings(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	settings.UpdatedAt = nowForWebsitePreview()
	record, err := settings.toModel(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	previous, err := prepareWebsiteWithTLSAndSettings(site, service.WebRoot, service.LogRoot, service.challengeRoot(), tlsOptions, previousRecord)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(site, service.WebRoot, service.LogRoot, service.challengeRoot(), tlsOptions, record)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	path, err := service.ConfigFile(site)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	before := ""
	if data, readErr := readBoundedFile(path, maxWebServerConfigBytes); readErr == nil {
		before = string(data)
	} else if !errors.Is(readErr, os.ErrNotExist) && site.Enabled {
		return WebsiteRuntimePreview{}, readErr
	}
	after := prepared.config
	if site.Enabled && before != "" {
		after = mergeWebsiteSettingsConfig(previous.config, prepared.config, before)
	}
	if site.Enabled {
		if err := service.validateRuntimeCandidate(path, after); err != nil {
			return WebsiteRuntimePreview{}, err
		}
	}
	version, err := service.RuntimeRevision(site.ID)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	return WebsiteRuntimePreview{
		Website: *site, BeforePath: path, AfterPath: path,
		BeforeContent: before, AfterContent: after, CurrentVersion: version,
		Reload: site.Enabled,
	}, nil
}

// PreviewManagedConfig validates a site-level raw configuration candidate
// without writing it or reloading the service.
func (service *Service) PreviewManagedConfig(ctx context.Context, id int64, content, revision string) (WebsiteRuntimePreview, error) {
	site, err := service.Get(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	manager, err := service.managedConfigManager()
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	relative, err := service.managedConfigRelativePath(manager, site)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	current, err := manager.Read(relative)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	if !strings.EqualFold(current.Revision, strings.TrimSpace(revision)) {
		return WebsiteRuntimePreview{}, fmt.Errorf("%w: configuration changed after it was opened", ErrWebServerConfigConflict)
	}
	if err := manager.ValidateContent(ctx, relative, content); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	version, err := service.RuntimeRevision(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	return WebsiteRuntimePreview{
		Website: *site, BeforePath: current.Path, AfterPath: current.Path,
		BeforeContent: current.Content, AfterContent: content,
		CurrentVersion: version, Reload: site.Enabled,
	}, nil
}

// PreviewToggle describes the virtual-host file change caused by enabling or
// disabling a website. Website data and content are never removed by this
// operation.
func (service *Service) PreviewToggle(id int64, enabled bool) (WebsiteRuntimePreview, error) {
	if err := service.validate(); err != nil {
		return WebsiteRuntimePreview{}, err
	}
	site, err := service.Get(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	path, err := service.ConfigFile(site)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	before := ""
	if data, readErr := readBoundedFile(path, maxWebServerConfigBytes); readErr == nil {
		before = string(data)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WebsiteRuntimePreview{}, readErr
	}
	after := ""
	if enabled {
		_, record, loadErr := service.loadSettings(id)
		if loadErr != nil {
			return WebsiteRuntimePreview{}, loadErr
		}
		tlsOptions, tlsErr := service.activeTLSOptions(site.ID, site.Domain)
		if tlsErr != nil {
			return WebsiteRuntimePreview{}, tlsErr
		}
		prepared, prepareErr := prepareWebsiteWithTLSAndSettings(site, service.WebRoot, service.LogRoot, service.challengeRoot(), tlsOptions, record)
		if prepareErr != nil {
			return WebsiteRuntimePreview{}, prepareErr
		}
		after = prepared.config
		if err := service.validateRuntimeCandidate(path, after); err != nil {
			return WebsiteRuntimePreview{}, err
		}
	}
	version, err := service.RuntimeRevision(id)
	if err != nil {
		return WebsiteRuntimePreview{}, err
	}
	return WebsiteRuntimePreview{
		Website: *site, BeforePath: path, AfterPath: path,
		BeforeContent: before, AfterContent: after,
		CurrentVersion: version, Reload: site.Enabled != enabled,
	}, nil
}

func (service *Service) validateRuntimeCandidate(path, content string) error {
	manager, err := service.managedConfigManager()
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(manager.Server.ConfigRoot, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("网站候选配置不属于当前 Web 服务器配置目录")
	}
	return manager.ValidateContent(context.Background(), filepath.ToSlash(relative), content)
}

func nowForWebsitePreview() (t time.Time) {
	return time.Now()
}
