package softwarecatalog

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

	"oneinstack/internal/services/scriptregistry"

	"gorm.io/gorm"
)

// VerifyBundledCatalog ties the local store inventory to the locked packages.
// Its revision describes local release bytes, not a Center signature.
func VerifyBundledCatalog(root string) (Document, scriptregistry.BundledProductionLock, error) {
	lock, err := scriptregistry.VerifyBundledProduction(root)
	if err != nil {
		return Document{}, lock, err
	}
	lockBytes, err := os.ReadFile(filepath.Join(root, "production-lock.json"))
	if err != nil {
		return Document{}, lock, err
	}
	catalogBytes, err := os.ReadFile(filepath.Join(root, "production-catalog.json"))
	if err != nil {
		return Document{}, lock, err
	}
	var products []Product
	if err := json.Unmarshal(catalogBytes, &products); err != nil {
		return Document{}, lock, fmt.Errorf("parse bundled production catalog: %w", err)
	}
	if len(products) != len(lock.Packages) {
		return Document{}, lock, fmt.Errorf("bundled catalog product count differs from production lock")
	}
	packages := make(map[string]scriptregistry.BundledPackageRecord, len(lock.Packages))
	for _, record := range lock.Packages {
		packages[record.Component] = record
	}
	seenKeys := make(map[string]bool, len(products))
	seenComponents := make(map[string]bool, len(products))
	for _, product := range products {
		if err := validateProduct(product); err != nil {
			return Document{}, lock, err
		}
		if seenKeys[product.Key] || seenComponents[product.Component] {
			return Document{}, lock, fmt.Errorf("duplicate bundled catalog product %s", product.Key)
		}
		seenKeys[product.Key] = true
		seenComponents[product.Component] = true
		record, ok := packages[product.Component]
		if !ok {
			return Document{}, lock, fmt.Errorf("bundled catalog component %s is not locked", product.Component)
		}
		manifest, _, err := scriptregistry.BundledPackageDigest(filepath.Join(root, record.Component, record.Version))
		if err != nil {
			return Document{}, lock, err
		}
		for _, version := range product.Versions {
			if version.Enabled && (!scriptregistry.SupportsSoftwareVersion(manifest.Component.SoftwareVersions, version.Version) || version.Channel != manifest.Component.Channel) {
				return Document{}, lock, fmt.Errorf("bundled %s does not support catalog version %s", product.Component, version.Version)
			}
		}
	}
	digest := sha256.Sum256(append(lockBytes, catalogBytes...))
	return Document{SchemaVersion: 1, Revision: hex.EncodeToString(digest[:]), Products: products}, lock, nil
}

// EnsureBundledCatalog installs the release inventory only before the first
// signed Center snapshot, or when a newer bundled release replaces an older one.
func (m *Manager) EnsureBundledCatalog(ctx context.Context) error {
	state, err := m.loadState()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state.Revision != "" && state.Mode != "bundled" {
		return nil
	}
	document, lock, err := VerifyBundledCatalog(m.config.BundledPath)
	if err != nil {
		return err
	}
	if state.Mode == "bundled" && state.Revision == document.Revision && state.Channel == m.config.Channel {
		complete, err := m.localCatalogComplete(state)
		if err != nil || complete {
			return err
		}
	}
	localConfig := m.config
	localConfig.Enabled = false
	registry, err := scriptregistry.New(localConfig)
	if err != nil {
		return err
	}
	packageVersions := make(map[string]string)
	for _, product := range document.Products {
		for _, version := range product.Versions {
			if !version.Enabled || version.Channel != m.config.Channel {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if _, err := registry.Resolve(ctx, product.Component, version.Version); err != nil {
				continue
			}
			for _, record := range lock.Packages {
				if record.Component == product.Component {
					packageVersions[packageVersionKey(product.Component, version.Version, version.Channel)] = record.Version
					break
				}
			}
		}
	}
	document.KeyID = ""
	return m.apply(document, packageVersions, packageVersions, "bundled")
}

// BundledCatalogPackage checks the precise local inventory identity used by
// installation preview and task recovery.
func BundledCatalogPackage(root, revision, component, packageVersion, digest string) bool {
	document, lock, err := VerifyBundledCatalog(root)
	if err != nil || document.Revision != strings.TrimSpace(revision) {
		return false
	}
	for _, record := range lock.Packages {
		if record.Component == component {
			return record.Version == packageVersion && record.SHA256 == digest
		}
	}
	return false
}
