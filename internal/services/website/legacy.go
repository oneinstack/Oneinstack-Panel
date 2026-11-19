package website

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/legacyoneinstack"
	"oneinstack/utils"

	"gorm.io/gorm"
)

const legacyConfigPurpose = "website.legacy-disabled-config"

type legacyFileMetadata struct {
	Mode uint32 `json:"mode"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
}

func rememberLegacyFileMetadata(site *models.Website, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() {
		return errors.New("original website config file metadata is unavailable")
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal([]byte(site.LegacyMetadata), &metadata); err != nil {
		return err
	}
	metadata["fileMetadata"], err = json.Marshal(legacyFileMetadata{Mode: uint32(info.Mode().Perm()), UID: int(owner.Uid), GID: int(owner.Gid)})
	if err != nil {
		return err
	}
	data, err := json.Marshal(metadata)
	if err == nil {
		site.LegacyMetadata = string(data)
	}
	return err
}

func preserveLegacyPublisherMetadata(publisher *Publisher, site *models.Website) error {
	var state struct {
		FileMetadata *legacyFileMetadata `json:"fileMetadata"`
	}
	if err := json.Unmarshal([]byte(site.LegacyMetadata), &state); err != nil {
		return err
	}
	publisher.preserveFileMetadata = true
	if metadata := state.FileMetadata; metadata != nil {
		if metadata.Mode & ^uint32(0777) != 0 || metadata.UID < 0 || metadata.GID < 0 {
			return errors.New("original website config file metadata is invalid")
		}
		publisher.restoredFileMetadata = map[string]configSnapshot{site.Name + ".conf": {mode: os.FileMode(metadata.Mode), uid: metadata.UID, gid: metadata.GID, ownerKnown: true}}
	}
	return nil
}

func legacyWebServerCandidates() []webServerCandidate {
	if app.DB() == nil {
		return nil
	}
	var rows []models.Software
	if app.DB().Where("installed = ? AND origin = ? AND runtime_group = ?", true, legacyoneinstack.Origin, "web-server").Find(&rows).Error != nil {
		return nil
	}
	result := []webServerCandidate{}
	for _, row := range rows {
		c, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
		if err != nil {
			continue
		}
		prefix := c.InstallDir
		if c.ID == "openresty" {
			prefix = filepath.Join(prefix, "nginx")
		}
		v, probeErr := legacyoneinstack.VerifyService(context.Background(), c)
		result = append(result, webServerCandidate{Component: c.ID, Name: c.Name, Service: c.Service, Binary: c.Binary, Prefix: prefix, Config: filepath.Dir(c.Config), Priority: 110, SystemdActive: probeErr == nil && v["ActiveState"] == "active"})
	}
	return result
}

func legacyWebServer(server WebServerInfo) (*legacyoneinstack.Component, error) {
	row, err := legacyoneinstack.Installed(app.DB(), server.Component)
	if err != nil || row == nil {
		return nil, err
	}
	c, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(c.Binary) != filepath.Clean(server.BinaryPath) {
		return nil, errors.New("legacy Web server executable changed")
	}
	return &c, nil
}

func protectLegacyTemplate(site *models.Website) error {
	if site != nil && site.Origin == legacyoneinstack.Origin {
		return fmt.Errorf("%w: 原有 OneinStack 站点保留完整配置，请使用网站的配置文件编辑；模板设置需要先完成显式转换", ErrWebsiteConfigUnavailable)
	}
	return nil
}

func legacyToggleConfig(site *models.Website, enabled bool) (string, error) {
	if !enabled {
		return "", nil
	}
	if site.Enabled {
		s, err := legacyoneinstack.DecodeSite(site.LegacyMetadata)
		if err != nil {
			return "", err
		}
		data, err := legacyoneinstack.ReadFile(s.Config)
		return string(data), err
	}
	if site.PreservedConfig == "" {
		return "", fmt.Errorf("%w: 原有站点缺少保留配置，不能使用默认模板重建", ErrWebsiteConfigUnavailable)
	}
	return utils.DecryptCredential(site.PreservedConfig, legacyConfigPurpose)
}

func enrichLegacyCertificate(site *models.Website) {
	if site.Origin != legacyoneinstack.Origin {
		return
	}
	s, err := legacyoneinstack.DecodeSite(site.LegacyMetadata)
	if err != nil || s.Certificate == "" {
		return
	}
	data, err := legacyoneinstack.ReadFile(s.Certificate)
	if err != nil {
		return
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return
	}
	site.SSLEnabled = site.Enabled
	site.CertificateStatus = "active"
	now := time.Now()
	if cert.NotAfter.Before(now) {
		site.CertificateStatus = "expired"
	} else if cert.NotAfter.Before(now.Add(30 * 24 * time.Hour)) {
		site.CertificateStatus = "expiring"
	}
	site.CertificateExpiresAt = &cert.NotAfter
}

func legacyLogPath(site models.Website, kind string) string {
	if site.Origin != legacyoneinstack.Origin {
		return ""
	}
	s, err := legacyoneinstack.DecodeSite(site.LegacyMetadata)
	if err != nil {
		return ""
	}
	path := s.AccessLog
	if kind == "error" {
		path = s.ErrorLog
	}
	if legacyoneinstack.ValidatePath(path) != nil {
		return ""
	}
	return path
}

func legacyWebsiteConfigPath(site *models.Website) (string, error) {
	s, err := legacyoneinstack.DecodeSite(site.LegacyMetadata)
	if err != nil {
		return "", err
	}
	if s.Name != site.Name || s.Engine != site.Engine || filepath.Base(s.Config) != site.Name+".conf" {
		return "", errors.New("legacy site ownership mismatch")
	}
	return s.Config, nil
}

// LegacyDeleteSnapshot captures the existing vhost metadata needed to restore
// a configuration-only deletion without rebuilding it from a Panel template.
func (service *Service) LegacyDeleteSnapshot(site *models.Website, deleteFiles bool) error {
	if site.Origin != legacyoneinstack.Origin {
		return nil
	}
	if deleteFiles {
		return fmt.Errorf("%w: 接管站点只支持删除配置并保留网站文件，原目录可能与其他站点共用", ErrWebsiteConfigUnavailable)
	}
	path, err := service.ConfigFile(site)
	if err != nil {
		return err
	}
	if !site.Enabled {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("%w: 已停用站点的原配置路径重新出现，请检查后再删除", ErrWebsiteConflict)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err := legacyToggleConfig(site, true)
		return err
	}
	return rememberLegacyFileMetadata(site, path)
}

// RestoreLegacyDeletion republishes the original vhost for a deleted site.
// Its document root and external certificate files are left in place.
func (service *Service) RestoreLegacyDeletion(ctx context.Context, site *models.Website, content string) error {
	webServerConfigMu.Lock()
	defer webServerConfigMu.Unlock()

	if err := service.validate(); err != nil {
		return err
	}
	if site == nil || site.ID <= 0 || site.Origin != legacyoneinstack.Origin || site.LegacyMetadata == "" {
		return fmt.Errorf("%w: 备份缺少接管站点的原始配置归属", ErrWebsiteConfigUnavailable)
	}
	if err := service.ensureWebsiteOwnerActive(site); err != nil {
		return err
	}
	owner, err := legacyoneinstack.Installed(service.DB, site.Engine)
	if err != nil {
		return err
	}
	if owner == nil {
		return fmt.Errorf("%w: 原接管组件已不可用", ErrWebsiteConfigUnavailable)
	}
	component, err := legacyoneinstack.DecodeComponent(owner.LegacyMetadata)
	if err != nil {
		return err
	}
	path, err := service.ConfigFile(site)
	if err != nil {
		return err
	}
	if component.VhostDir != filepath.Dir(path) {
		return fmt.Errorf("%w: 原配置路径与当前接管组件不匹配", ErrWebsiteConfigUnavailable)
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: 原网站配置路径已被占用", ErrWebsiteConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var count int64
	if err := service.DB.Model(&models.Website{}).Where("id = ? OR name = ? OR domain = ?", site.ID, site.Name, site.Domain).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("%w: 网站记录已存在", ErrWebsiteConflict)
	}
	root, err := service.ManagedRoot(site)
	if err != nil {
		return err
	}
	if root != "" {
		if info, err := os.Lstat(root); err != nil || !info.IsDir() {
			return fmt.Errorf("%w: 原网站根目录不可用，请先从快照恢复网站文件", ErrWebsiteConfigUnavailable)
		}
	}
	if !site.Enabled {
		if _, err := legacyToggleConfig(site, true); err != nil {
			return err
		}
		return service.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(site).Error; err != nil {
				return err
			}
			return tx.Model(site).UpdateColumn("enabled", false).Error
		})
	}
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("%w: 备份缺少原始站点配置", ErrWebsiteConfigUnavailable)
	}
	if err := service.validateRuntimeCandidate(site, path, content); err != nil {
		return err
	}
	publisher, err := service.publisherForSite(site)
	if err != nil {
		return err
	}
	publisher.requireMissingConfig = true
	publication, err := publisher.Publish(ctx, map[string]*string{filepath.Base(path): &content})
	if err != nil {
		return err
	}
	if err := service.DB.WithContext(ctx).Create(site).Error; err != nil {
		return errors.Join(err, publication.Rollback(context.Background()))
	}
	return nil
}

func isLegacyProxyChain(candidates []webServerCandidate) bool {
	return len(candidates) == 2 && legacyoneinstack.IsProxyPair(app.DB(), candidates[0].Component, candidates[1].Component)
}
