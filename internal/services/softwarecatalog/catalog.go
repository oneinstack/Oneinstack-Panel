package softwarecatalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"oneinstack/config"
	"oneinstack/internal/models"
	"oneinstack/internal/panelidentity"
	"oneinstack/internal/services/scriptregistry"

	"gorm.io/gorm"
)

const (
	signatureDomain       = "oneinstack-software-catalog-v1\n"
	maxCatalogBytes       = 8 << 20
	panelInstanceIDHeader = "X-Oneinstack-Panel-Instance-ID"
)

var (
	identifier          = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	softwareVersion     = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	softwareVersionLine = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+)*\.x$`)
	serviceName         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
	runtimeGroup        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

type Parameter struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Rule        string `json:"rule,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Default     string `json:"default,omitempty"`
}

type Version struct {
	Version            string `json:"version"`
	Line               string `json:"line,omitempty"`
	Channel            string `json:"channel"`
	Enabled            bool   `json:"enabled"`
	Recommended        bool   `json:"recommended,omitempty"`
	AllowCustomVersion bool   `json:"allowCustomVersion,omitempty"`
	Order              int    `json:"order,omitempty"`
	ReleaseNotes       string `json:"releaseNotes,omitempty"`
}

type Product struct {
	Key          string      `json:"key"`
	Component    string      `json:"component"`
	Name         string      `json:"name"`
	Description  string      `json:"description,omitempty"`
	Icon         string      `json:"icon,omitempty"`
	Type         string      `json:"type,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	ManageScopes []string    `json:"manageScopes,omitempty"`
	ServiceName  string      `json:"serviceName,omitempty"`
	RuntimeGroup string      `json:"runtimeGroup,omitempty"`
	Visible      bool        `json:"visible"`
	Installable  bool        `json:"installable"`
	Order        int         `json:"order,omitempty"`
	Versions     []Version   `json:"versions"`
	Parameters   []Parameter `json:"parameters,omitempty"`
}

type Document struct {
	SchemaVersion int       `json:"schemaVersion"`
	Revision      string    `json:"revision"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Products      []Product `json:"products"`
	KeyID         string    `json:"keyId"`
	Signature     string    `json:"signature"`
}

type unsignedDocument struct {
	SchemaVersion int       `json:"schemaVersion"`
	GeneratedAt   time.Time `json:"generatedAt"`
	Products      []Product `json:"products"`
}

type Status struct {
	Enabled                 bool       `json:"enabled"`
	Mode                    string     `json:"mode"`
	Revision                string     `json:"revision,omitempty"`
	KeyID                   string     `json:"keyId,omitempty"`
	ProductCount            int        `json:"productCount"`
	VersionCount            int        `json:"versionCount"`
	InstallableProductCount int        `json:"installableProductCount"`
	InstallableVersionCount int        `json:"installableVersionCount"`
	MissingPackageCount     int        `json:"missingPackageCount"`
	LastSyncedAt            *time.Time `json:"lastSyncedAt,omitempty"`
	LastAttemptAt           *time.Time `json:"lastAttemptAt,omitempty"`
	LastError               string     `json:"lastError,omitempty"`
	Stale                   bool       `json:"stale"`
	Channel                 string     `json:"channel"`
}

func encodeManageScopes(scopes []string) string {
	if len(scopes) == 0 {
		return ""
	}
	contents, err := json.Marshal(scopes)
	if err != nil {
		return ""
	}
	return string(contents)
}

func decodeManageScopes(contents string) []string {
	contents = strings.TrimSpace(contents)
	if contents == "" {
		return nil
	}
	var scopes []string
	if err := json.Unmarshal([]byte(contents), &scopes); err != nil {
		return nil
	}
	return scopes
}

type Manager struct {
	config     config.ScriptCenter
	instanceID string
	db         *gorm.DB
	client     *http.Client
	baseURL    *url.URL
	now        func() time.Time
}

func New(centerConfig config.ScriptCenter, db *gorm.DB) (*Manager, error) {
	return NewWithInstanceID(centerConfig, db, "")
}

func NewWithInstanceID(centerConfig config.ScriptCenter, db *gorm.DB, instanceID string) (*Manager, error) {
	if db == nil {
		return nil, errors.New("software catalog database is required")
	}
	var baseURL *url.URL
	var err error
	if centerConfig.Enabled {
		baseURL, err = url.Parse(strings.TrimRight(strings.TrimSpace(centerConfig.URL), "/"))
		if err != nil {
			return nil, fmt.Errorf("parse software catalog Center URL: %w", err)
		}
		if baseURL.Host == "" {
			return nil, errors.New("software catalog Center URL must include a host")
		}
	}
	return &Manager{
		config:     centerConfig,
		instanceID: strings.TrimSpace(instanceID),
		db:         db,
		client: &http.Client{
			Timeout: time.Duration(centerConfig.RequestTimeoutSeconds) * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		baseURL: baseURL,
		now:     time.Now,
	}, nil
}

func (m *Manager) Sync(ctx context.Context) (Status, error) {
	if !m.config.Enabled {
		return m.Status()
	}
	state, _ := m.loadState()
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(m.baseURL.String(), "/")+"/v1/software/catalog",
		nil,
	)
	if err != nil {
		return m.failSync(err)
	}
	if state.Revision != "" && state.Channel == m.config.Channel {
		request.Header.Set("If-None-Match", `"`+state.Revision+`"`)
	}
	if m.instanceID != "" {
		request.Header.Set(panelInstanceIDHeader, m.instanceID)
	}
	if networkInfo := panelidentity.HeaderValue(); networkInfo != "" {
		request.Header.Set(panelidentity.NetworkInfoHeader, networkInfo)
	}
	forcedRefresh := false
	for {
		response, err := m.client.Do(request)
		if err != nil {
			return m.failSync(fmt.Errorf("request Center software catalog: %w", err))
		}
		if response.StatusCode == http.StatusNotModified {
			response.Body.Close()
			complete, checkErr := m.localCatalogComplete(state)
			if checkErr != nil {
				return m.failSync(fmt.Errorf("check local software catalog: %w", checkErr))
			}
			if complete {
				if refreshErr := m.refreshPackageVersions(ctx); refreshErr != nil {
					return m.failSync(fmt.Errorf("refresh component package versions: %w", refreshErr))
				}
				packageCounts, countErr := m.refreshPackageAvailability(ctx, state.Channel, state.Revision)
				if countErr != nil {
					return m.failSync(fmt.Errorf("refresh component package counts: %w", countErr))
				}
				state.InstallableProductCount = packageCounts.InstallableProductCount
				state.InstallableVersionCount = packageCounts.InstallableVersionCount
				state.MissingPackageCount = packageCounts.MissingPackageCount
				now := m.now().UTC()
				state.ID = 1
				state.Mode = "center"
				state.Channel = m.config.Channel
				state.LastAttemptAt = &now
				state.LastSyncedAt = &now
				state.LastError = ""
				state.UpdatedAt = now
				if err := m.db.Save(&state).Error; err != nil {
					return Status{}, err
				}
				return m.Status()
			}
			if forcedRefresh {
				return m.failSync(errors.New("Center returned not modified but local software catalog is incomplete"))
			}
			// The revision is unchanged, but the local offline rows are missing
			// or incomplete. Re-fetch the full signed document to repair them.
			request.Header.Del("If-None-Match")
			forcedRefresh = true
			continue
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return m.failSync(decodeAPIError(response))
		}
		var document Document
		if err := decodeLimitedJSON(response.Body, &document); err != nil {
			return m.failSync(fmt.Errorf("decode Center software catalog: %w", err))
		}
		if err := m.verify(document); err != nil {
			return m.failSync(fmt.Errorf("verify Center software catalog: %w", err))
		}
		packageVersions := m.resolvePackageVersions(ctx, document)
		publishedPackageVersions := m.resolvePublishedPackageVersions(ctx, document)
		if err := m.apply(document, packageVersions, publishedPackageVersions); err != nil {
			return m.failSync(fmt.Errorf("apply Center software catalog: %w", err))
		}
		return m.Status()
	}
}

func (m *Manager) localCatalogComplete(state models.SoftwareCatalogState) (bool, error) {
	var count int64
	err := m.db.Model(&models.Software{}).
		Where("catalog_managed = ? AND catalog_channel = ? AND catalog_revision = ?", true, state.Channel, state.Revision).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count == int64(state.VersionCount), nil
}

func (m *Manager) Status() (Status, error) {
	state, err := m.loadState()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return Status{}, err
	}
	status := Status{
		Enabled:                 m.config.Enabled,
		Mode:                    state.Mode,
		Revision:                state.Revision,
		KeyID:                   state.KeyID,
		ProductCount:            state.ProductCount,
		VersionCount:            state.VersionCount,
		InstallableProductCount: state.InstallableProductCount,
		InstallableVersionCount: state.InstallableVersionCount,
		MissingPackageCount:     state.MissingPackageCount,
		LastSyncedAt:            state.LastSyncedAt,
		LastAttemptAt:           state.LastAttemptAt,
		LastError:               state.LastError,
		Channel:                 m.config.Channel,
	}
	if status.Mode == "" {
		status.Mode = "local"
	}
	if state.LastSyncedAt != nil {
		status.Stale = m.now().After(state.LastSyncedAt.Add(
			time.Duration(m.config.CatalogStaleAfterHours) * time.Hour,
		))
	}
	if !m.config.Enabled {
		if state.Revision != "" {
			status.Mode = "center-cache-disabled"
		} else {
			status.Mode = "local"
		}
		return status, nil
	}
	if state.Revision == "" {
		status.Mode = "local-fallback"
		status.Stale = true
	} else if state.LastError != "" || status.Stale {
		status.Mode = "center-cache"
	}
	return status, nil
}

func (m *Manager) apply(document Document, packageVersions, publishedPackageVersions map[string]string) error {
	now := m.now().UTC()
	productCount := 0
	versionCount := 0
	installableProductCount := 0
	installableVersionCount := 0
	missingPackageCount := 0
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := backfillInstalledPackageVersions(tx); err != nil {
			return err
		}
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Model(&models.Software{}).
			Updates(map[string]any{
				"catalog_managed":        true,
				"catalog_visible":        false,
				"installable":            false,
				"recommended":            false,
				"is_update":              false,
				"latest_package_version": "",
			}).Error; err != nil {
			return err
		}

		for _, product := range document.Products {
			recommendedVersion := ""
			applicableVersions := make([]Version, 0, len(product.Versions))
			for _, version := range product.Versions {
				if version.Channel != m.config.Channel {
					continue
				}
				applicableVersions = append(applicableVersions, version)
				if version.Recommended && version.Enabled {
					recommendedVersion = version.Version
				}
			}
			if len(applicableVersions) == 0 {
				continue
			}
			productCount++
			productHasInstallableVersion := false
			parameters, err := json.Marshal(product.Parameters)
			if err != nil {
				return err
			}
			for _, version := range applicableVersions {
				versionCount++
				key := packageVersionKey(product.Component, version.Version, version.Channel)
				packagePublished := publishedPackageVersions[key] != ""
				packageAvailable := packageVersions[key] != ""
				if !packagePublished {
					missingPackageCount++
				}
				if product.Installable && version.Enabled && packageAvailable {
					installableVersionCount++
					productHasInstallableVersion = true
				}
				var row models.Software
				result := tx.Where("`key` = ? AND version = ?", product.Key, version.Version).First(&row)
				if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
					return result.Error
				}
				values := map[string]any{
					"name":                   product.Name,
					"component":              product.Component,
					"describe":               product.Description,
					"type":                   product.Type,
					"tags":                   strings.Join(product.Tags, ","),
					"manage_scopes":          encodeManageScopes(product.ManageScopes),
					"service_name":           strings.TrimSpace(product.ServiceName),
					"runtime_group":          strings.TrimSpace(product.RuntimeGroup),
					"params":                 string(parameters),
					"resource":               "center",
					"catalog_managed":        true,
					"catalog_channel":        version.Channel,
					"catalog_visible":        product.Visible && version.Enabled,
					"installable":            product.Installable && version.Enabled,
					"recommended":            version.Recommended,
					"version_line":           version.Line,
					"allow_custom_version":   version.AllowCustomVersion,
					"catalog_order":          product.Order,
					"version_order":          version.Order,
					"catalog_revision":       document.Revision,
					"release_notes":          version.ReleaseNotes,
					"latest_package_version": packageVersions[packageVersionKey(product.Component, version.Version, version.Channel)],
				}
				if product.Icon != "" {
					values["icon"] = product.Icon
				}
				if errors.Is(result.Error, gorm.ErrRecordNotFound) {
					row = models.Software{
						Key:            product.Key,
						Version:        version.Version,
						Status:         models.Soft_Status_Default,
						CatalogVisible: true,
						Installable:    true,
					}
					if err := tx.Create(&row).Error; err != nil {
						return err
					}
				}
				if err := tx.Model(&models.Software{}).
					Where("id = ?", row.Id).
					Updates(values).Error; err != nil {
					return err
				}
			}
			if productHasInstallableVersion {
				installableProductCount++
			}
			if recommendedVersion != "" {
				var installed models.Software
				if err := tx.Where("`key` = ? AND installed = ?", product.Key, true).
					First(&installed).Error; err == nil {
					installedVersion := strings.TrimSpace(installed.InstallVersion)
					if installedVersion == "" {
						installedVersion = installed.Version
					}
					latestPackageVersion := packageVersions[packageVersionKey(product.Component, recommendedVersion, m.config.Channel)]
					installedPackageVersion := strings.TrimSpace(installed.InstalledPackageVersion)
					// A catalog recommendation is an upgrade only when it is newer
					// than the version already installed on the host.  A stale
					// recommendation must never turn into a downgrade prompt.
					softwareUpdate := scriptregistry.ComparePackageVersions(recommendedVersion, installedVersion) > 0
					packageUpdate := installedPackageVersion != "" && latestPackageVersion != "" &&
						scriptregistry.ComparePackageVersions(latestPackageVersion, installedPackageVersion) > 0
					if softwareUpdate || packageUpdate {
						if err := tx.Model(&models.Software{}).
							Where("`key` = ?", product.Key).
							Update("is_update", true).Error; err != nil {
							return err
						}
					}
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
		}
		state := models.SoftwareCatalogState{
			ID:                      1,
			Mode:                    "center",
			Channel:                 m.config.Channel,
			Revision:                document.Revision,
			KeyID:                   document.KeyID,
			ProductCount:            productCount,
			VersionCount:            versionCount,
			InstallableProductCount: installableProductCount,
			InstallableVersionCount: installableVersionCount,
			MissingPackageCount:     missingPackageCount,
			LastSyncedAt:            &now,
			LastAttemptAt:           &now,
			LastError:               "",
			UpdatedAt:               now,
		}
		return tx.Save(&state).Error
	})
	return err
}

func backfillInstalledPackageVersions(tx *gorm.DB) error {
	if !tx.Migrator().HasTable(&models.SoftwareTask{}) {
		return nil
	}
	var installedRows []models.Software
	if err := tx.Where(
		"installed = ? AND COALESCE(installed_package_version, '') = ''",
		true,
	).Find(&installedRows).Error; err != nil {
		return err
	}
	for _, installed := range installedRows {
		var task models.SoftwareTask
		err := tx.Where(
			"software_key = ? AND status = ? AND operation IN ? AND COALESCE(resolved_version, '') <> ''",
			installed.Key,
			models.SoftwareTaskStatusSucceeded,
			[]string{"install", "upgrade"},
		).Order("finished_at DESC, created_at DESC").First(&task).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&models.Software{}).
			Where("id = ?", installed.Id).
			Update("installed_package_version", strings.TrimSpace(task.ResolvedVersion)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) resolvePackageVersions(ctx context.Context, document Document) map[string]string {
	resolved := make(map[string]string)
	registry, err := scriptregistry.New(m.config)
	if err != nil {
		return resolved
	}
	requests := make([]scriptregistry.PackageBatchRequest, 0)
	for _, product := range document.Products {
		for _, version := range product.Versions {
			if version.Channel != m.config.Channel {
				continue
			}
			key := packageVersionKey(product.Component, version.Version, version.Channel)
			if _, exists := resolved[key]; exists {
				continue
			}
			requests = append(requests, scriptregistry.PackageBatchRequest{
				Component: product.Component, SoftwareVersion: version.Version, Channel: version.Channel,
			})
		}
	}
	requests = dedupePackageBatchRequests(requests)
	versions, batchErr := registry.ResolvePackageVersionChannelBatch(ctx, requests)
	if errors.Is(batchErr, scriptregistry.ErrBatchUnsupported) {
		versions = make(map[string]string, len(requests))
		for _, item := range requests {
			version, resolveErr := registry.ResolvePackageVersionChannel(ctx, item.Component, item.SoftwareVersion, item.Channel)
			if resolveErr == nil {
				versions[packageVersionKey(item.Component, item.SoftwareVersion, item.Channel)] = version
			}
		}
		batchErr = nil
	}
	if batchErr == nil {
		for key, version := range versions {
			resolved[key] = version
		}
	}
	return resolved
}

func (m *Manager) resolvePublishedPackageVersions(ctx context.Context, document Document) map[string]string {
	resolved := make(map[string]string)
	registry, err := scriptregistry.New(m.config)
	if err != nil {
		return resolved
	}
	requests := make([]scriptregistry.PackageBatchRequest, 0)
	for _, product := range document.Products {
		for _, version := range product.Versions {
			if version.Channel != m.config.Channel {
				continue
			}
			requests = append(requests, scriptregistry.PackageBatchRequest{
				Component: product.Component, SoftwareVersion: version.Version, Channel: version.Channel,
			})
		}
	}
	requests = dedupePackageBatchRequests(requests)
	available, batchErr := registry.PackageAvailableChannelBatch(ctx, requests)
	if errors.Is(batchErr, scriptregistry.ErrBatchUnsupported) {
		available = make(map[string]bool, len(requests))
		for _, item := range requests {
			ok, availabilityErr := registry.PackageAvailableChannel(ctx, item.Component, item.SoftwareVersion, item.Channel)
			if availabilityErr == nil {
				available[packageVersionKey(item.Component, item.SoftwareVersion, item.Channel)] = ok
			}
		}
		batchErr = nil
	}
	if batchErr == nil {
		for key, ok := range available {
			if ok {
				parts := strings.Split(key, "\x00")
				if len(parts) == 3 {
					resolved[key] = parts[1]
				}
			}
		}
	}
	return resolved
}

type packageAvailabilityCounts struct {
	InstallableProductCount int
	InstallableVersionCount int
	MissingPackageCount     int
}

func (m *Manager) refreshPackageAvailability(ctx context.Context, channel, revision string) (packageAvailabilityCounts, error) {
	var rows []models.Software
	if err := m.db.Where(
		"catalog_managed = ? AND catalog_channel = ? AND catalog_revision = ?",
		true, channel, revision,
	).Find(&rows).Error; err != nil {
		return packageAvailabilityCounts{}, err
	}
	registry, err := scriptregistry.New(m.config)
	if err != nil {
		return packageAvailabilityCounts{}, err
	}
	counts := packageAvailabilityCounts{}
	installableProducts := make(map[string]struct{})
	requests := make([]scriptregistry.PackageBatchRequest, 0, len(rows))
	for _, row := range rows {
		requests = append(requests, scriptregistry.PackageBatchRequest{
			Component: row.Component, SoftwareVersion: row.Version, Channel: row.CatalogChannel,
		})
	}
	requests = dedupePackageBatchRequests(requests)
	if len(requests) == 0 {
		log.Printf("software catalog: skip Center package counts request rows=%d uniqueRequests=0", len(rows))
		return counts, nil
	}
	log.Printf("software catalog: package counts availability rows=%d uniqueRequests=%d", len(rows), len(requests))
	available, availabilityErr := registry.PackageAvailableChannelBatch(ctx, requests)
	if errors.Is(availabilityErr, scriptregistry.ErrBatchUnsupported) {
		available = make(map[string]bool, len(requests))
		for _, item := range requests {
			ok, itemErr := registry.PackageAvailableChannel(ctx, item.Component, item.SoftwareVersion, item.Channel)
			if itemErr == nil {
				available[packageVersionKey(item.Component, item.SoftwareVersion, item.Channel)] = ok
			}
		}
		availabilityErr = nil
	}
	if availabilityErr != nil {
		return counts, availabilityErr
	}
	availableCount := 0
	for _, ok := range available {
		if ok {
			availableCount++
		}
	}
	resolveRequests := make([]scriptregistry.PackageBatchRequest, 0)
	for _, row := range rows {
		key := packageVersionKey(row.Component, row.Version, row.CatalogChannel)
		if !available[key] {
			counts.MissingPackageCount++
			continue
		}
		resolveRequests = append(resolveRequests, scriptregistry.PackageBatchRequest{
			Component: row.Component, SoftwareVersion: row.Version, Channel: row.CatalogChannel,
		})
	}
	resolveRequests = dedupePackageBatchRequests(resolveRequests)
	log.Printf("software catalog: package counts available=%d resolveRequests=%d", availableCount, len(resolveRequests))
	packageVersions, resolveErr := registry.ResolvePackageVersionChannelBatch(ctx, resolveRequests)
	if errors.Is(resolveErr, scriptregistry.ErrBatchUnsupported) {
		packageVersions = make(map[string]string, len(resolveRequests))
		for _, item := range resolveRequests {
			version, itemErr := registry.ResolvePackageVersionChannel(ctx, item.Component, item.SoftwareVersion, item.Channel)
			if itemErr == nil {
				packageVersions[packageVersionKey(item.Component, item.SoftwareVersion, item.Channel)] = version
			}
		}
		resolveErr = nil
	}
	if resolveErr != nil {
		return counts, resolveErr
	}
	for _, row := range rows {
		key := packageVersionKey(row.Component, row.Version, row.CatalogChannel)
		if row.Installable && strings.TrimSpace(packageVersions[key]) != "" {
			counts.InstallableVersionCount++
			installableProducts[row.Key] = struct{}{}
		}
	}
	counts.InstallableProductCount = len(installableProducts)
	return counts, nil
}

func packageVersionKey(component, softwareVersion, channel string) string {
	return component + "\x00" + softwareVersion + "\x00" + channel
}

func dedupePackageBatchRequests(requests []scriptregistry.PackageBatchRequest) []scriptregistry.PackageBatchRequest {
	if len(requests) < 2 {
		return requests
	}
	seen := make(map[string]struct{}, len(requests))
	unique := make([]scriptregistry.PackageBatchRequest, 0, len(requests))
	for _, request := range requests {
		key := packageVersionKey(request.Component, request.SoftwareVersion, request.Channel)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, request)
	}
	return unique
}

func (m *Manager) refreshPackageVersions(ctx context.Context) error {
	var installedRows []models.Software
	if err := m.db.Where(
		"installed = ?",
		true,
	).Find(&installedRows).Error; err != nil {
		return err
	}
	rows := make([]models.Software, 0, len(installedRows))
	for _, installed := range installedRows {
		var recommended models.Software
		err := m.db.Where(
			"`key` = ? AND catalog_managed = ? AND installable = ? AND recommended = ?",
			installed.Key,
			true,
			true,
			true,
		).First(&recommended).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		rows = append(rows, recommended)
	}
	registry, err := scriptregistry.New(m.config)
	if err != nil {
		return err
	}
	resolved := make(map[int]string, len(rows))
	for _, row := range rows {
		resolved[row.Id] = ""
		packageVersion, resolveErr := registry.ResolvePackageVersionChannel(
			ctx,
			row.Component,
			row.Version,
			row.CatalogChannel,
		)
		if resolveErr == nil {
			resolved[row.Id] = packageVersion
		}
	}
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := backfillInstalledPackageVersions(tx); err != nil {
			return err
		}
		for rowID, packageVersion := range resolved {
			if err := tx.Model(&models.Software{}).
				Where("id = ?", rowID).
				Update("latest_package_version", packageVersion).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.Software{}).
			Where("catalog_managed = ?", true).
			Update("is_update", false).Error; err != nil {
			return err
		}
		installedRows = nil
		if err := tx.Where("installed = ?", true).Find(&installedRows).Error; err != nil {
			return err
		}
		for _, installed := range installedRows {
			var recommended models.Software
			err := tx.Where(
				"`key` = ? AND catalog_managed = ? AND installable = ? AND recommended = ?",
				installed.Key,
				true,
				true,
				true,
			).First(&recommended).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			installedVersion := strings.TrimSpace(installed.InstallVersion)
			if installedVersion == "" {
				installedVersion = strings.TrimSpace(installed.Version)
			}
			packageUpdate := installed.InstalledPackageVersion != "" &&
				recommended.LatestPackageVersion != "" &&
				scriptregistry.ComparePackageVersions(
					recommended.LatestPackageVersion,
					installed.InstalledPackageVersion,
				) > 0
			if scriptregistry.ComparePackageVersions(recommended.Version, installedVersion) > 0 || packageUpdate {
				if err := tx.Model(&models.Software{}).
					Where("`key` = ?", installed.Key).
					Update("is_update", true).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (m *Manager) verify(document Document) error {
	if document.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schemaVersion %d", document.SchemaVersion)
	}
	publicKeyEncoded, trusted := m.config.TrustedKeys[document.KeyID]
	if !trusted {
		return fmt.Errorf("software catalog signing key %s is not trusted", document.KeyID)
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyEncoded))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("trusted software catalog key %s is invalid", document.KeyID)
	}
	if len(document.Revision) != sha256.Size*2 {
		return errors.New("software catalog revision is invalid")
	}
	seen := make(map[string]struct{}, len(document.Products))
	for _, product := range document.Products {
		if err := validateProduct(product); err != nil {
			return err
		}
		if _, exists := seen[product.Key]; exists {
			return fmt.Errorf("duplicate software product %q", product.Key)
		}
		seen[product.Key] = struct{}{}
	}
	unsigned := unsignedDocument{
		SchemaVersion: document.SchemaVersion,
		GeneratedAt:   document.GeneratedAt,
		Products:      document.Products,
	}
	payload, err := json.Marshal(unsigned)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	if actual := hex.EncodeToString(digest[:]); actual != document.Revision {
		return errors.New("software catalog revision does not match its payload")
	}
	signature, err := base64.StdEncoding.DecodeString(document.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(publicKey), signaturePayload(document.Revision), signature) {
		return errors.New("software catalog signature verification failed")
	}
	return nil
}

func validateProduct(product Product) error {
	if !identifier.MatchString(product.Key) || !identifier.MatchString(product.Component) {
		return errors.New("software key and component must be lowercase identifiers")
	}
	if len(product.Name) < 1 || len(product.Name) > 128 || len(product.Description) > 2048 ||
		len(product.Icon) > 65536 || len(product.Tags) > 16 ||
		len(product.Versions) < 1 || len(product.Versions) > 64 ||
		len(product.Parameters) > 32 {
		return fmt.Errorf("software product %s contains invalid metadata", product.Key)
	}
	if product.ServiceName != "" && !serviceName.MatchString(product.ServiceName) {
		return fmt.Errorf("software product %s contains an invalid service name", product.Key)
	}
	if product.RuntimeGroup != "" && !runtimeGroup.MatchString(product.RuntimeGroup) {
		return fmt.Errorf("software product %s contains an invalid runtime group", product.Key)
	}
	if product.RuntimeGroup != "" && product.ServiceName == "" {
		return fmt.Errorf("software product %s runtime group requires a service name", product.Key)
	}
	versions := make(map[string]struct{}, len(product.Versions))
	recommendedByChannel := make(map[string]int)
	enabledByChannel := make(map[string]int)
	for _, version := range product.Versions {
		if !softwareVersion.MatchString(version.Version) {
			return fmt.Errorf("software product %s has invalid version %q", product.Key, version.Version)
		}
		if version.Line != "" && !softwareVersionLine.MatchString(version.Line) {
			return fmt.Errorf("software product %s has invalid version line %q", product.Key, version.Line)
		}
		if version.AllowCustomVersion && version.Line == "" {
			return fmt.Errorf("software product %s custom versions require a version line", product.Key)
		}
		switch version.Channel {
		case "stable", "beta", "development":
		default:
			return fmt.Errorf("software product %s has invalid channel %q", product.Key, version.Channel)
		}
		identity := version.Channel + "\x00" + version.Version
		if _, exists := versions[identity]; exists {
			return fmt.Errorf("software product %s has duplicate version %q", product.Key, version.Version)
		}
		versions[identity] = struct{}{}
		if version.Enabled {
			enabledByChannel[version.Channel]++
		}
		if version.Recommended {
			if !version.Enabled {
				return fmt.Errorf("recommended version %s must be enabled", version.Version)
			}
			recommendedByChannel[version.Channel]++
		}
	}
	if product.Installable {
		for channel, enabled := range enabledByChannel {
			if enabled > 0 && recommendedByChannel[channel] != 1 {
				return fmt.Errorf(
					"software product %s must have exactly one recommended %s version",
					product.Key,
					channel,
				)
			}
		}
	}
	for _, parameter := range product.Parameters {
		if !identifier.MatchString(strings.ToLower(parameter.Key)) ||
			len(parameter.Name) < 1 || len(parameter.Name) > 128 {
			return fmt.Errorf("software product %s contains an invalid parameter", product.Key)
		}
		switch parameter.Type {
		case "input", "password", "number", "port", "path", "boolean", "username":
		default:
			return fmt.Errorf(
				"software product %s contains unsupported parameter type %q",
				product.Key,
				parameter.Type,
			)
		}
	}
	return nil
}

func (m *Manager) failSync(syncErr error) (Status, error) {
	now := m.now().UTC()
	state, _ := m.loadState()
	state.ID = 1
	if state.Mode == "" {
		state.Mode = "local"
	}
	state.LastAttemptAt = &now
	state.LastError = truncate(syncErr.Error(), 1024)
	state.UpdatedAt = now
	if err := m.db.Save(&state).Error; err != nil {
		return Status{}, errors.Join(syncErr, err)
	}
	status, statusErr := m.Status()
	if statusErr != nil {
		return Status{}, errors.Join(syncErr, statusErr)
	}
	return status, syncErr
}

func (m *Manager) loadState() (models.SoftwareCatalogState, error) {
	var state models.SoftwareCatalogState
	err := m.db.First(&state, 1).Error
	return state, err
}

func decodeLimitedJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, maxCatalogBytes+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("software catalog contains multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeAPIError(response *http.Response) error {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if json.Unmarshal(bytes.TrimSpace(body), &envelope) == nil && envelope.Error.Message != "" {
		return errors.New(envelope.Error.Message)
	}
	return fmt.Errorf("Center software catalog returned HTTP %d", response.StatusCode)
}

func signaturePayload(revision string) []byte {
	return []byte(signatureDomain + revision + "\n")
}

func truncate(value string, size int) string {
	if len(value) <= size {
		return value
	}
	return value[:size]
}
