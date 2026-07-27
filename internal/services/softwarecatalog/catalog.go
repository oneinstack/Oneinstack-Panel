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
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"oneinstack/config"
	"oneinstack/internal/models"

	"gorm.io/gorm"
)

const (
	signatureDomain = "oneinstack-software-catalog-v1\n"
	maxCatalogBytes = 8 << 20
)

var (
	identifier      = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	softwareVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
)

type Parameter struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Rule        string `json:"rule,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
}

type Version struct {
	Version      string `json:"version"`
	Channel      string `json:"channel"`
	Enabled      bool   `json:"enabled"`
	Recommended  bool   `json:"recommended,omitempty"`
	Order        int    `json:"order,omitempty"`
	ReleaseNotes string `json:"releaseNotes,omitempty"`
}

type Product struct {
	Key         string      `json:"key"`
	Component   string      `json:"component"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Icon        string      `json:"icon,omitempty"`
	Type        string      `json:"type,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Visible     bool        `json:"visible"`
	Installable bool        `json:"installable"`
	Order       int         `json:"order,omitempty"`
	Versions    []Version   `json:"versions"`
	Parameters  []Parameter `json:"parameters,omitempty"`
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
	Enabled       bool       `json:"enabled"`
	Mode          string     `json:"mode"`
	Revision      string     `json:"revision,omitempty"`
	KeyID         string     `json:"keyId,omitempty"`
	ProductCount  int        `json:"productCount"`
	VersionCount  int        `json:"versionCount"`
	LastSyncedAt  *time.Time `json:"lastSyncedAt,omitempty"`
	LastAttemptAt *time.Time `json:"lastAttemptAt,omitempty"`
	LastError     string     `json:"lastError,omitempty"`
	Stale         bool       `json:"stale"`
	Channel       string     `json:"channel"`
}

type Manager struct {
	config  config.ScriptCenter
	db      *gorm.DB
	client  *http.Client
	baseURL *url.URL
	now     func() time.Time
}

func New(centerConfig config.ScriptCenter, db *gorm.DB) (*Manager, error) {
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
		config: centerConfig,
		db:     db,
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
	if state.Revision != "" {
		request.Header.Set("If-None-Match", `"`+state.Revision+`"`)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return m.failSync(fmt.Errorf("request Center software catalog: %w", err))
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		now := m.now().UTC()
		state.ID = 1
		state.Mode = "center"
		state.LastAttemptAt = &now
		state.LastSyncedAt = &now
		state.LastError = ""
		state.UpdatedAt = now
		if err := m.db.Save(&state).Error; err != nil {
			return Status{}, err
		}
		return m.Status()
	}
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
	if err := m.apply(document); err != nil {
		return m.failSync(fmt.Errorf("apply Center software catalog: %w", err))
	}
	return m.Status()
}

func (m *Manager) Status() (Status, error) {
	state, err := m.loadState()
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return Status{}, err
	}
	status := Status{
		Enabled:       m.config.Enabled,
		Mode:          state.Mode,
		Revision:      state.Revision,
		KeyID:         state.KeyID,
		ProductCount:  state.ProductCount,
		VersionCount:  state.VersionCount,
		LastSyncedAt:  state.LastSyncedAt,
		LastAttemptAt: state.LastAttemptAt,
		LastError:     state.LastError,
		Channel:       m.config.Channel,
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

func (m *Manager) apply(document Document) error {
	now := m.now().UTC()
	productCount := 0
	versionCount := 0
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Model(&models.Software{}).
			Updates(map[string]any{
				"catalog_managed": true,
				"catalog_visible": false,
				"installable":     false,
				"recommended":     false,
				"is_update":       false,
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
			parameters, err := json.Marshal(product.Parameters)
			if err != nil {
				return err
			}
			for _, version := range applicableVersions {
				versionCount++
				var row models.Software
				result := tx.Where("`key` = ? AND version = ?", product.Key, version.Version).First(&row)
				if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
					return result.Error
				}
				values := map[string]any{
					"name":             product.Name,
					"component":        product.Component,
					"describe":         product.Description,
					"type":             product.Type,
					"tags":             strings.Join(product.Tags, ","),
					"params":           string(parameters),
					"resource":         "center",
					"catalog_managed":  true,
					"catalog_visible":  product.Visible && version.Enabled,
					"installable":      product.Installable && version.Enabled,
					"recommended":      version.Recommended,
					"catalog_order":    product.Order,
					"version_order":    version.Order,
					"catalog_revision": document.Revision,
					"release_notes":    version.ReleaseNotes,
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
			if recommendedVersion != "" {
				var installed models.Software
				if err := tx.Where("`key` = ? AND installed = ?", product.Key, true).
					First(&installed).Error; err == nil {
					installedVersion := strings.TrimSpace(installed.InstallVersion)
					if installedVersion == "" {
						installedVersion = installed.Version
					}
					if installedVersion != recommendedVersion {
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
			ID:            1,
			Mode:          "center",
			Revision:      document.Revision,
			KeyID:         document.KeyID,
			ProductCount:  productCount,
			VersionCount:  versionCount,
			LastSyncedAt:  &now,
			LastAttemptAt: &now,
			LastError:     "",
			UpdatedAt:     now,
		}
		return tx.Save(&state).Error
	})
	return err
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
	versions := make(map[string]struct{}, len(product.Versions))
	recommendedByChannel := make(map[string]int)
	enabledByChannel := make(map[string]int)
	for _, version := range product.Versions {
		if !softwareVersion.MatchString(version.Version) {
			return fmt.Errorf("software product %s has invalid version %q", product.Key, version.Version)
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
