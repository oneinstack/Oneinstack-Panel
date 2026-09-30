package scriptregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const productionLockName = "production-lock.json"

var productionComponents = []string{
	"adminer", "apache", "caddy", "clamav", "docker", "docker-compose",
	"fail2ban", "firewalld", "halo", "mariadb", "mongodb", "mysql",
	"nginx", "nodejs", "openresty", "opensearch", "php", "phpmyadmin",
	"redis", "tengine", "tomcat", "webdav",
}

type BundledPackageRecord struct {
	Component string `json:"component"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
}

type BundledProductionLock struct {
	SchemaVersion int                    `json:"schemaVersion"`
	CenterCommit  string                 `json:"centerCommit"`
	Packages      []BundledPackageRecord `json:"packages"`
}

// BundledPackageDigest pins the validated directory contents. Center packages
// checksum every payload file except the manifest, so both files are included
// in this digest. validateDirectory also verifies action execute permissions.
func BundledPackageDigest(root string) (Manifest, string, error) {
	manifest, err := validateDirectory(root)
	if err != nil {
		return Manifest{}, "", err
	}
	manifestBytes, err := os.ReadFile(filepath.Join(root, manifestFileName))
	if err != nil {
		return Manifest{}, "", err
	}
	checksumBytes, err := os.ReadFile(filepath.Join(root, checksumFileName))
	if err != nil {
		return Manifest{}, "", err
	}
	manifestHash := sha256.Sum256(manifestBytes)
	checksumHash := sha256.Sum256(checksumBytes)
	payload := fmt.Sprintf("%x  %s\n%x  %s\n", manifestHash, manifestFileName, checksumHash, checksumFileName)
	digest := sha256.Sum256([]byte(payload))
	return manifest, hex.EncodeToString(digest[:]), nil
}

func loadProductionLock(root string) (BundledProductionLock, error) {
	contents, err := os.ReadFile(filepath.Join(root, productionLockName))
	if err != nil {
		return BundledProductionLock{}, err
	}
	var lock BundledProductionLock
	if err := json.Unmarshal(contents, &lock); err != nil {
		return BundledProductionLock{}, fmt.Errorf("parse bundled production lock: %w", err)
	}
	if lock.SchemaVersion != 1 || len(lock.Packages) != len(productionComponents) ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(lock.CenterCommit) {
		return BundledProductionLock{}, fmt.Errorf("invalid bundled production lock header")
	}
	seen := make(map[string]bool, len(lock.Packages))
	for _, record := range lock.Packages {
		allowed := false
		for _, component := range productionComponents {
			if record.Component == component {
				allowed = true
				break
			}
		}
		if !allowed || seen[record.Component] || !versionPattern.MatchString(record.Version) ||
			len(record.SHA256) != sha256.Size*2 || strings.ToLower(record.SHA256) != record.SHA256 {
			return BundledProductionLock{}, fmt.Errorf("invalid bundled production package entry")
		}
		if _, err := hex.DecodeString(record.SHA256); err != nil {
			return BundledProductionLock{}, fmt.Errorf("invalid bundled production package digest: %w", err)
		}
		seen[record.Component] = true
	}
	return lock, nil
}

// VerifyBundledProduction checks the release inventory and each selected
// package, including the manifest, script checksums, and fixed identity.
func VerifyBundledProduction(root string) (BundledProductionLock, error) {
	lock, err := loadProductionLock(root)
	if err != nil {
		return BundledProductionLock{}, err
	}
	for _, record := range lock.Packages {
		manifest, digest, err := BundledPackageDigest(filepath.Join(root, record.Component, record.Version))
		if err != nil {
			return BundledProductionLock{}, fmt.Errorf("verify bundled %s: %w", record.Component, err)
		}
		if manifest.Component.ID != record.Component || manifest.Component.Version != record.Version ||
			manifest.Component.Channel != "stable" || digest != record.SHA256 {
			return BundledProductionLock{}, fmt.Errorf("bundled %s identity differs from production lock", record.Component)
		}
	}
	return lock, nil
}

func (r *Registry) resolveProductionBundled(component, softwareVersion string) (Package, error) {
	lock, err := loadProductionLock(r.config.BundledPath)
	if err != nil {
		return Package{}, fmt.Errorf("read bundled production lock: %w", err)
	}
	for _, record := range lock.Packages {
		if record.Component != component {
			continue
		}
		root := filepath.Join(r.config.BundledPath, component, record.Version)
		manifest, digest, err := BundledPackageDigest(root)
		if err != nil {
			return Package{}, fmt.Errorf("verify bundled %s package: %w", component, err)
		}
		if digest != record.SHA256 || manifest.Component.ID != component || manifest.Component.Version != record.Version ||
			manifest.Component.Channel != r.config.Channel || !manifest.supportsSoftwareVersion(softwareVersion) ||
			!compatibleWithHost(manifest, r.host) || !manifest.sourceAvailableForHost(softwareVersion, r.host) {
			return Package{}, fmt.Errorf("no compatible locked bundled %s package for software version %s", component, softwareVersion)
		}
		return Package{Manifest: manifest, Root: root, Source: "bundled", Metadata: Metadata{Manifest: manifest, SHA256: digest}}, nil
	}
	return Package{}, fmt.Errorf("no locked bundled %s package", component)
}
