package website

import (
	"context"
	"errors"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	safeservice "oneinstack/internal/services/safe"
	"oneinstack/router/input"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Service struct {
	DB              *gorm.DB
	WebRoot         string
	LogRoot         string
	ChallengeRoot   string
	CertificateRoot string
	VhostRoot       string
	Publisher       *Publisher
	ConfigManager   *WebServerConfigManager
	Firewall        *safeservice.Service
}

func defaultService() (*Service, error) {
	if app.DB() == nil {
		return nil, errors.New("database is not initialized")
	}
	server, err := DetectWebServer()
	if err != nil {
		return nil, err
	}
	vhostRoot := strings.TrimSpace(app.ONE_CONFIG.System.WebVhostRoot)
	if vhostRoot == "" {
		vhostRoot = filepath.Join(app.GetBasePath(), "vhost")
	}
	if err := ensureManagedVhostLayout(app.DB(), server, vhostRoot); err != nil {
		return nil, fmt.Errorf("prepare Web server vhost layout: %w", err)
	}
	return &Service{
		DB:              app.DB(),
		WebRoot:         app.ONE_CONFIG.System.WebPath,
		LogRoot:         app.ONE_CONFIG.System.LogPath,
		ChallengeRoot:   app.ONE_CONFIG.System.ACMEChallengePath,
		CertificateRoot: app.ONE_CONFIG.System.CertificatePath,
		VhostRoot:       vhostRoot,
		Publisher: &Publisher{
			ConfigDir:      filepath.Join(vhostRoot, server.Component),
			NginxBinary:    server.BinaryPath,
			Engine:         server.Component,
			ServiceName:    server.ServiceName,
			MainConfigPath: server.MainConfigPath,
			Runner:         OSCommandRunner{},
		},
		ConfigManager: newWebServerConfigManager(server),
		Firewall:      safeservice.NewDefaultService(),
	}, nil
}

const managedVhostMigrationMarker = ".oneinstack-vhost-migrated"

func ensureManagedVhostLayout(database *gorm.DB, server WebServerInfo, vhostRoot string) error {
	root, err := normalizedBasePath(vhostRoot, "Web server vhost root")
	if err != nil {
		return err
	}
	engine, err := normalizeWebsiteEngine(server.Component)
	if err != nil {
		return err
	}
	engineRoot := filepath.Join(root, engine)
	if err := os.MkdirAll(engineRoot, 0750); err != nil {
		return err
	}
	mainPath := filepath.Clean(server.MainConfigPath)
	var originalMain []byte
	mainExisted := isRegularFile(mainPath)
	if mainExisted {
		originalMain, err = os.ReadFile(mainPath)
		if err != nil {
			return err
		}
	}
	includeChanged, err := ensureEngineVhostInclude(server, engineRoot)
	if err != nil {
		return err
	}
	restoreMain := func(cause error) error {
		if !includeChanged || !mainExisted {
			return cause
		}
		if restoreErr := atomicWriteConfig(filepath.Dir(mainPath), mainPath, originalMain); restoreErr != nil {
			return fmt.Errorf("%w; restore Web server main configuration failed: %v", cause, restoreErr)
		}
		return cause
	}
	marker := filepath.Join(root, managedVhostMigrationMarker)
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return restoreMain(err)
	}
	if err := migrateManagedVhostFiles(database, server, engineRoot); err != nil {
		return restoreMain(err)
	}
	if err := os.WriteFile(marker, []byte("version=1\n"), 0640); err != nil {
		return restoreMain(err)
	}
	return nil
}

func ensureEngineVhostInclude(server WebServerInfo, engineRoot string) (bool, error) {
	mainPath := filepath.Clean(server.MainConfigPath)
	if !isRegularFile(mainPath) {
		return false, nil
	}
	content, err := os.ReadFile(mainPath)
	if err != nil {
		return false, err
	}
	includePath := filepath.ToSlash(engineRoot) + "/*.conf"
	if strings.Contains(string(content), includePath) {
		return false, nil
	}
	updated := string(content)
	switch server.Component {
	case "nginx", "openresty", "tengine":
		include := "\n    include " + includePath + ";\n"
		if index := strings.Index(updated, "http {"); index >= 0 {
			position := index + len("http {")
			updated = updated[:position] + include + updated[position:]
		} else {
			return false, errors.New("Nginx main configuration has no http block for managed vhosts")
		}
	case "apache":
		updated += "\nIncludeOptional " + includePath + "\n"
	case "caddy":
		updated += "\nimport " + includePath + "\n"
	default:
		return false, fmt.Errorf("unsupported Web server engine %q", server.Component)
	}
	if err := atomicWriteConfig(filepath.Dir(mainPath), mainPath, []byte(updated)); err != nil {
		return false, err
	}
	return true, nil
}

func migrateManagedVhostFiles(database *gorm.DB, server WebServerInfo, engineRoot string) error {
	if database == nil || strings.TrimSpace(server.SiteConfigDir) == "" {
		return nil
	}
	var sites []models.Website
	if err := database.Select("name, engine").Find(&sites).Error; err != nil {
		return err
	}
	vhostRoot := filepath.Dir(engineRoot)
	type movedFile struct {
		source string
		target string
	}
	moved := make([]movedFile, 0)
	createdTargets := make([]string, 0)
	rollback := func(cause error) error {
		var rollbackErr error
		for index := len(createdTargets) - 1; index >= 0; index-- {
			if err := os.Remove(createdTargets[index]); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		for index := len(moved) - 1; index >= 0; index-- {
			if err := os.Rename(moved[index].target, moved[index].source); err != nil {
				rollbackErr = errors.Join(rollbackErr, err)
			}
		}
		if rollbackErr != nil {
			return fmt.Errorf("%w; restore migrated Web server configuration failed: %v", cause, rollbackErr)
		}
		return cause
	}
	for index := range sites {
		name := strings.TrimSpace(sites[index].Name)
		if !configNamePattern.MatchString(name + ".conf") {
			continue
		}
		engine, engineErr := normalizeWebsiteEngine(sites[index].Engine)
		if engineErr != nil {
			engine, engineErr = normalizeWebsiteEngine(server.Component)
			if engineErr != nil {
				return rollback(engineErr)
			}
		}
		legacy := filepath.Join(filepath.Clean(server.SiteConfigDir), name+".conf")
		info, err := os.Lstat(legacy)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return rollback(err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		targetRoot := filepath.Join(vhostRoot, engine)
		if err := os.MkdirAll(targetRoot, 0750); err != nil {
			return rollback(err)
		}
		target := filepath.Join(targetRoot, name+".conf")
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(err)
		}
		data, err := os.ReadFile(legacy)
		if err != nil {
			return rollback(err)
		}
		if err := atomicWriteConfig(targetRoot, target, data); err != nil {
			return rollback(err)
		}
		createdTargets = append(createdTargets, target)
		legacyRoot := filepath.Join(vhostRoot, ".legacy", engine)
		if err := os.MkdirAll(legacyRoot, 0700); err != nil {
			return rollback(err)
		}
		if err := os.Rename(legacy, filepath.Join(legacyRoot, name+".conf")); err != nil {
			return rollback(err)
		}
		moved = append(moved, movedFile{source: legacy, target: filepath.Join(legacyRoot, name+".conf")})
	}
	return nil
}

// DefaultService returns the configured website service for background task
// managers. Callers must still use the service methods so paths and Nginx
// publication remain validated.
func DefaultService() (*Service, error) {
	return defaultService()
}

func (service *Service) Get(id int64) (*models.Website, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	if id <= 0 {
		return nil, ErrWebsiteIDRequired
	}
	var site models.Website
	if err := service.DB.First(&site, "id = ?", id).Error; err != nil {
		return nil, err
	}
	// Reading an existing website must remain possible when its content root
	// was removed outside Panel. Mutation paths perform the actual filesystem
	// checks/creation as needed; keep the managed-path and symlink boundaries
	// while allowing a missing configured root here so operation previews and
	// repair-oriented reads stay consistent with the lifecycle executor.
	if !strings.EqualFold(strings.TrimSpace(site.Type), "proxy") {
		if _, err := validateManagedPathWithOptions(service.WebRoot, site.RootDir, true); err != nil {
			return nil, err
		}
	}
	return &site, nil
}

func (service *Service) ConfigFile(site *models.Website) (string, error) {
	if err := service.validate(); err != nil {
		return "", err
	}
	if site == nil {
		return "", errors.New("website is required")
	}
	name := strings.TrimSpace(site.Name) + ".conf"
	if !configNamePattern.MatchString(name) {
		return "", errors.New("stored website has an unsafe Web server config name")
	}
	configRoot, err := service.websiteConfigRoot(site)
	if err != nil {
		return "", err
	}
	return filepath.Join(configRoot, name), nil
}

func (service *Service) websiteConfigRoot(site *models.Website) (string, error) {
	if service == nil || service.Publisher == nil {
		return "", errors.New("Web server publisher is not configured")
	}
	if strings.TrimSpace(service.VhostRoot) == "" {
		return normalizedBasePath(service.Publisher.ConfigDir, "Web server config root")
	}
	engine, err := normalizeWebsiteEngine(site.Engine)
	if err != nil {
		return "", err
	}
	root, err := normalizedBasePath(service.VhostRoot, "Web server vhost root")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, engine), nil
}

func (service *Service) publisherForSite(site *models.Website) (*Publisher, error) {
	if service == nil || service.Publisher == nil {
		return nil, errors.New("Web server publisher is not configured")
	}
	engine, err := normalizeWebsiteEngine(site.Engine)
	if err != nil {
		return nil, err
	}
	publisher := *service.Publisher
	publisher.Engine = engine
	configRoot, err := service.websiteConfigRoot(site)
	if err != nil {
		return nil, err
	}
	publisher.ConfigDir = configRoot
	currentEngine, _ := normalizeWebsiteEngine(service.Publisher.Engine)
	if currentEngine != engine {
		publisher.NginxBinary = webEngineBinary(engine)
		publisher.ServiceName = webServiceName(engine)
		publisher.MainConfigPath = webEngineMainConfig(engine)
	}
	return &publisher, nil
}

func webEngineBinary(engine string) string {
	switch engine {
	case "openresty":
		return "/usr/local/openresty/nginx/sbin/nginx"
	case "tengine":
		return "/usr/local/tengine/sbin/nginx"
	case "apache":
		return "/usr/local/apache/bin/httpd"
	case "caddy":
		return "/usr/local/caddy/bin/caddy"
	default:
		return "/usr/local/nginx/sbin/nginx"
	}
}

func webEngineMainConfig(engine string) string {
	root := filepath.Dir(webEngineBinary(engine))
	switch engine {
	case "openresty":
		return filepath.Join(root, "../conf/nginx.conf")
	case "apache":
		return filepath.Join(root, "../conf/httpd.conf")
	case "caddy":
		return filepath.Join(root, "../conf/Caddyfile")
	default:
		return filepath.Join(root, "../conf/nginx.conf")
	}
}

// ManagedRoot verifies that a stored website root is strictly below WebRoot
// and that no existing path component is a symbolic link.
func (service *Service) ManagedRoot(site *models.Website) (string, error) {
	if site == nil {
		return "", errors.New("website is required")
	}
	if strings.EqualFold(strings.TrimSpace(site.Type), "proxy") {
		return "", nil
	}
	root := filepath.Clean(strings.TrimSpace(site.RootDir))
	if root == "." || !filepath.IsAbs(root) {
		return "", errors.New("stored website root is not an absolute managed path")
	}
	return validateManagedPath(service.WebRoot, root)
}

func (service *Service) prepareCreate(param *models.Website, allowManagedAbsolute, allowMissingManagedRoot bool) (*preparedWebsite, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	if param == nil {
		return nil, errors.New("website parameters are required")
	}
	if strings.TrimSpace(param.Engine) == "" && service.Publisher != nil {
		param.Engine = service.Publisher.Engine
	}
	prepared, err := prepareWebsiteForCreate(
		param,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		TLSOptions{},
		allowManagedAbsolute,
	)
	if err != nil {
		return nil, wrapWebsiteParameterError(err)
	}
	if prepared.model.Type != "proxy" {
		if _, err := validateManagedPathWithOptions(service.WebRoot, prepared.model.RootDir, allowMissingManagedRoot); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrWebsiteRootInvalid, err)
		}
	}
	return prepared, nil
}

// PrepareCreate validates and normalizes a website without changing system
// state. The returned model is the exact shape that should be reviewed and
// later passed to Add.
func (service *Service) PrepareCreate(param *models.Website) (*models.Website, error) {
	prepared, err := service.prepareCreate(param, false, true)
	if err != nil {
		return nil, err
	}
	result := prepared.model
	return &result, nil
}

// RestoreSnapshot creates or updates a site through the normal validated
// renderer. Raw archived Nginx text is retained for audit/integrity purposes
// but is never installed directly.
func (service *Service) RestoreSnapshot(ctx context.Context, snapshot *models.Website) error {
	if snapshot == nil || snapshot.ID <= 0 {
		return errors.New("website snapshot is invalid")
	}
	var existing models.Website
	err := service.DB.First(&existing, "id = ?", snapshot.ID).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		copy := *snapshot
		return service.Add(ctx, &copy)
	case err != nil:
		return err
	default:
		copy := *snapshot
		return service.Update(ctx, &copy)
	}
}

func List(param *input.WebsiteQueryParam) (*services.PaginatedResult[models.Website], error) {
	tx := app.DB()
	if param.Name != "" {
		tx = tx.Where("name like ?", "%"+param.Name+"%")
	}
	if param.Domain != "" {
		tx = tx.Where("domain like ?", "%"+param.Domain+"%")
	}
	if param.Type != "" {
		tx = tx.Where("type = ?", param.Type)
	}
	result, err := services.Paginate[models.Website](tx, &models.Website{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
	if err != nil || len(result.Data) == 0 {
		return result, err
	}
	websiteIDs := make([]int64, 0, len(result.Data))
	for i := range result.Data {
		websiteIDs = append(websiteIDs, result.Data[i].ID)
	}
	var certificates []models.Certificate
	if err := app.DB().Where("website_id IN ?", websiteIDs).Find(&certificates).Error; err != nil {
		return nil, err
	}
	certificatesByWebsite := make(map[int64]models.Certificate, len(certificates))
	for _, certificate := range certificates {
		certificatesByWebsite[certificate.WebsiteID] = certificate
	}
	var traffic []models.WebsiteTrafficDaily
	today := time.Now().Format("2006-01-02")
	if err := app.DB().
		Where("website_id IN ? AND day = ?", websiteIDs, today).
		Find(&traffic).Error; err != nil {
		return nil, err
	}
	trafficByWebsite := make(map[int64]models.WebsiteTrafficDaily, len(traffic))
	for _, record := range traffic {
		trafficByWebsite[record.WebsiteID] = record
	}
	now := time.Now().UTC()
	warningAt := now.Add(time.Duration(app.ONE_CONFIG.System.CertificateExpiryWarningDays) * 24 * time.Hour)
	for i := range result.Data {
		if record, exists := trafficByWebsite[result.Data[i].ID]; exists {
			result.Data[i].TodayTrafficBytes = record.BytesSent
			result.Data[i].TodayRequests = record.RequestCount
		}
		certificate, exists := certificatesByWebsite[result.Data[i].ID]
		if !exists || certificate.Status == models.CertificateStatusDisabled {
			continue
		}
		result.Data[i].SSLEnabled = certificate.Status == models.CertificateStatusActive ||
			certificate.Status == models.CertificateStatusExpiring ||
			certificate.Status == models.CertificateStatusExpired
		result.Data[i].CertificateStatus = certificate.Status
		if certificate.NotAfter.Before(now) {
			result.Data[i].CertificateStatus = models.CertificateStatusExpired
		} else if certificate.NotAfter.Before(warningAt) {
			result.Data[i].CertificateStatus = models.CertificateStatusExpiring
		}
		expiresAt := certificate.NotAfter
		result.Data[i].CertificateExpiresAt = &expiresAt
	}
	return result, nil
}

func Add(param *models.Website) error {
	service, err := defaultService()
	if err != nil {
		return err
	}
	return service.Add(context.Background(), param)
}

func (service *Service) Add(ctx context.Context, param *models.Website) error {
	return service.add(ctx, param, false)
}

// AddPrepared executes a server-normalized website create payload from an
// operation preview. Absolute roots are accepted only after the same managed
// root and no-symlink checks as normal creation.
func (service *Service) AddPrepared(ctx context.Context, param *models.Website) error {
	return service.add(ctx, param, true)
}

func (service *Service) add(ctx context.Context, param *models.Website, allowManagedAbsolute bool) error {
	if err := service.validate(); err != nil {
		return err
	}
	if param == nil {
		return errors.New("website parameters are required")
	}
	param.Enabled = true
	param.DisabledReason = ""
	if err := normalizeWebsiteExpiration(param, time.Now()); err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(param.Type), "proxy") {
		// prepareCreate validates the normalized managed path before the child
		// directory is created, so initialize the trusted base for fresh setups.
		if err := os.MkdirAll(filepath.Clean(service.WebRoot), 0755); err != nil {
			return fmt.Errorf("create managed website root: %w", err)
		}
	}
	prepared, err := service.prepareCreate(param, allowManagedAbsolute, false)
	if err != nil {
		return err
	}
	publisher, err := service.publisherForSite(&prepared.model)
	if err != nil {
		return err
	}
	createdRoot, err := service.ensureWebsiteRoot(prepared.model.Type, prepared.model.RootDir)
	if err != nil {
		return err
	}
	defaultPagePath, createdDefaultPage, err := ensureDefaultWebsitePage(&prepared.model)
	if err != nil {
		cleanupWebsiteAddFiles(prepared.model.RootDir, defaultPagePath, createdRoot, createdDefaultPage)
		return err
	}
	var firewallRuleID int64
	var createdFirewallRule bool
	if service.Firewall != nil {
		firewallRuleID, createdFirewallRule, err = service.Firewall.EnsureWebsitePort(
			ctx,
			prepared.listenPort,
			prepared.model.Name,
		)
		if err != nil {
			cleanupWebsiteAddFiles(prepared.model.RootDir, defaultPagePath, createdRoot, createdDefaultPage)
			return fmt.Errorf("自动放行网站端口 %d 失败: %w", prepared.listenPort, err)
		}
	}
	var publication *Publication
	transactionErr := service.DB.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&models.Website{}).
			Where("name = ? OR domain = ?", prepared.model.Name, prepared.model.Domain).
			Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return fmt.Errorf("%w: 网站 %s 已存在", ErrWebsiteConflict, prepared.model.Name)
		}
		if err := tx.Create(&prepared.model).Error; err != nil {
			return err
		}
		content := prepared.config
		published, err := publisher.Publish(ctx, map[string]*string{
			prepared.configName: &content,
		})
		if err != nil {
			return err
		}
		publication = published
		return nil
	})
	if transactionErr != nil {
		if publication != nil {
			transactionErr = errors.Join(transactionErr, publication.Rollback(context.Background()))
		}
		if createdFirewallRule {
			transactionErr = errors.Join(
				transactionErr,
				service.Firewall.Delete(context.Background(), firewallRuleID),
			)
		}
		cleanupWebsiteAddFiles(prepared.model.RootDir, defaultPagePath, createdRoot, createdDefaultPage)
		return transactionErr
	}
	*param = prepared.model
	return nil
}

func Update(param *models.Website) error {
	service, err := defaultService()
	if err != nil {
		return err
	}
	return service.Update(context.Background(), param)
}

func (service *Service) Update(ctx context.Context, param *models.Website) error {
	if err := service.validate(); err != nil {
		return err
	}
	if param == nil || param.ID <= 0 {
		return ErrWebsiteIDRequired
	}
	if err := validateWebsiteRootInput(service.WebRoot, param.RootDir, param.Dir, true); err != nil {
		return err
	}
	var existing models.Website
	if err := service.DB.First(&existing, "id = ?", param.ID).Error; err != nil {
		return err
	}
	if strings.TrimSpace(param.Engine) == "" {
		param.Engine = existing.Engine
		if strings.TrimSpace(param.Engine) == "" && service.Publisher != nil {
			param.Engine = service.Publisher.Engine
		}
	}
	_, settings, err := service.loadSettings(existing.ID)
	if err != nil {
		return err
	}
	previousTLSOptions, err := service.activeTLSOptions(existing.ID, existing.Domain)
	if err != nil {
		return err
	}
	previous, err := prepareWebsiteWithTLSAndSettings(
		&existing,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		previousTLSOptions,
		settings,
	)
	if err != nil {
		return err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(
		param,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		TLSOptions{},
		settings,
	)
	if err != nil {
		return wrapWebsiteParameterError(err)
	}
	prepared.model.ID = existing.ID
	prepared.model.CreateTime = existing.CreateTime
	prepared.model.Enabled = existing.Enabled
	prepared.model.DisabledReason = existing.DisabledReason
	if err := normalizeWebsiteExpiration(&prepared.model, time.Now()); err != nil {
		return err
	}
	tlsOptions, err := service.activeTLSOptions(existing.ID, prepared.model.Domain)
	if err != nil {
		return err
	}
	if tlsOptions.Enabled {
		prepared, err = prepareWebsiteWithTLSAndSettings(
			param,
			service.WebRoot,
			service.LogRoot,
			service.challengeRoot(),
			tlsOptions,
			settings,
		)
		if err != nil {
			return wrapWebsiteParameterError(err)
		}
		prepared.model.ID = existing.ID
		prepared.model.CreateTime = existing.CreateTime
		prepared.model.Enabled = existing.Enabled
		prepared.model.DisabledReason = existing.DisabledReason
		if err := normalizeWebsiteExpiration(&prepared.model, time.Now()); err != nil {
			return err
		}
	}
	createdRoot, err := service.ensureWebsiteRoot(prepared.model.Type, prepared.model.RootDir)
	if err != nil {
		return err
	}
	oldName := existing.Name + ".conf"
	if !configNamePattern.MatchString(oldName) {
		if createdRoot {
			_ = os.Remove(prepared.model.RootDir)
		}
		return errors.New("stored website has an unsafe Nginx config name")
	}
	oldPublisher, err := service.publisherForSite(&existing)
	if err != nil {
		return err
	}
	newPublisher, err := service.publisherForSite(&prepared.model)
	if err != nil {
		return err
	}
	oldEngine, _ := normalizeWebsiteEngine(existing.Engine)
	newEngine, _ := normalizeWebsiteEngine(prepared.model.Engine)
	if oldEngine != newEngine {
		oldConfigPath := filepath.Join(oldPublisher.ConfigDir, oldName)
		if current, readErr := os.ReadFile(oldConfigPath); readErr == nil &&
			strings.TrimSpace(string(current)) != strings.TrimSpace(previous.config) {
			return errors.New("UNSUPPORTED_ENGINE_DIRECTIVE: existing custom Web server directives must be migrated explicitly before changing engines")
		} else if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
	}

	publications := make([]*Publication, 0, 2)
	transactionErr := service.DB.Transaction(func(tx *gorm.DB) error {
		var duplicate int64
		if err := tx.Model(&models.Website{}).
			Where("id <> ? AND (name = ? OR domain = ?)", existing.ID, prepared.model.Name, prepared.model.Domain).
			Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return fmt.Errorf("%w: 网站 %s 已存在", ErrWebsiteConflict, prepared.model.Name)
		}
		if err := tx.Save(&prepared.model).Error; err != nil {
			return err
		}
		if filepath.Clean(oldPublisher.ConfigDir) == filepath.Clean(newPublisher.ConfigDir) {
			changes := map[string]*string{prepared.configName: nil}
			if prepared.model.Enabled {
				content := prepared.config
				merged, mergeErr := service.preserveCustomWebsiteConfig(&existing, previous.config, content)
				if mergeErr != nil {
					return mergeErr
				}
				content = merged
				changes[prepared.configName] = &content
			}
			if oldName != prepared.configName {
				changes[oldName] = nil
			}
			published, publishErr := newPublisher.Publish(ctx, changes)
			if publishErr != nil {
				return publishErr
			}
			publications = append(publications, published)
			return nil
		}

		oldPublished, publishErr := oldPublisher.Publish(ctx, map[string]*string{oldName: nil})
		if publishErr != nil {
			return publishErr
		}
		publications = append(publications, oldPublished)
		if prepared.model.Enabled {
			content := prepared.config
			newPublished, publishErr := newPublisher.Publish(ctx, map[string]*string{prepared.configName: &content})
			if publishErr != nil {
				return publishErr
			}
			publications = append(publications, newPublished)
		}
		return nil
	})
	if transactionErr != nil {
		for index := len(publications) - 1; index >= 0; index-- {
			transactionErr = errors.Join(transactionErr, publications[index].Rollback(context.Background()))
		}
		if createdRoot {
			_ = os.Remove(prepared.model.RootDir)
		}
		return transactionErr
	}
	*param = prepared.model
	return nil
}

func Delete(id int64) error {
	service, err := defaultService()
	if err != nil {
		return err
	}
	return service.Delete(context.Background(), id)
}

func (service *Service) Delete(ctx context.Context, id int64) error {
	return service.DeleteWithOptions(ctx, id, false)
}

// DeleteWithOptions removes the vhost and certificate state. Website files are
// retained by default. When requested, managed directories are first renamed
// to private tombstones so a failed database/Nginx transaction can restore
// them atomically.
func (service *Service) DeleteWithOptions(ctx context.Context, id int64, deleteFiles bool) error {
	if err := service.validate(); err != nil {
		return err
	}
	if id <= 0 {
		return ErrWebsiteIDRequired
	}
	var existing models.Website
	if err := service.DB.First(&existing, "id = ?", id).Error; err != nil {
		return err
	}
	configName := existing.Name + ".conf"
	if !configNamePattern.MatchString(configName) {
		return errors.New("stored website has an unsafe Nginx config name")
	}
	rootPath, err := service.ManagedRoot(&existing)
	if err != nil {
		return err
	}
	var activeTasks int64
	if err := service.DB.Model(&models.CertificateTask{}).
		Where("website_id = ? AND status IN ?", id, models.ActiveCertificateTaskStatuses()).
		Count(&activeTasks).Error; err != nil {
		return err
	}
	if activeTasks > 0 {
		return errors.New("网站存在运行中的证书任务，请先等待或取消任务")
	}
	certificateRoot := filepath.Clean(strings.TrimSpace(service.CertificateRoot))
	if certificateRoot == "." ||
		certificateRoot == string(filepath.Separator) ||
		!filepath.IsAbs(certificateRoot) {
		certificateRoot = filepath.Clean(filepath.Join(app.GetBasePath(), "certificates"))
	}
	certificateSiteDirectory := filepath.Join(certificateRoot, "sites", strconv.FormatInt(id, 10))
	if _, statErr := os.Lstat(certificateRoot); statErr == nil {
		certificateSiteDirectory, err = validateManagedPath(certificateRoot, certificateSiteDirectory)
		if err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	type stagedDirectory struct {
		original  string
		tombstone string
	}
	staged := make([]stagedDirectory, 0, 2)
	stage := func(base, path string) error {
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return statErr
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refuse to delete unmanaged directory %s", path)
		}
		tombstoneRoot := filepath.Join(filepath.Clean(base), ".oneinstack-delete")
		if err := os.MkdirAll(tombstoneRoot, 0700); err != nil {
			return err
		}
		if _, err := validateManagedPath(base, tombstoneRoot); err != nil {
			return err
		}
		tombstone := filepath.Join(tombstoneRoot, uuid.NewString())
		if err := os.Rename(path, tombstone); err != nil {
			return fmt.Errorf("stage directory deletion: %w", err)
		}
		staged = append(staged, stagedDirectory{original: path, tombstone: tombstone})
		return nil
	}
	restoreStaged := func() error {
		var result error
		for i := len(staged) - 1; i >= 0; i-- {
			if err := os.Rename(staged[i].tombstone, staged[i].original); err != nil {
				result = errors.Join(result, err)
			}
		}
		return result
	}
	if deleteFiles && rootPath != "" {
		if err := stage(service.WebRoot, rootPath); err != nil {
			return fmt.Errorf("prepare website file deletion: %w", err)
		}
	}
	if err := stage(certificateRoot, certificateSiteDirectory); err != nil {
		return errors.Join(fmt.Errorf("prepare certificate deletion: %w", err), restoreStaged())
	}
	var publication *Publication
	transactionErr := service.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.CertificateTask{}).
			Where("website_id = ? AND status IN ?", id, models.ActiveCertificateTaskStatuses()).
			Count(&activeTasks).Error; err != nil {
			return err
		}
		if activeTasks > 0 {
			return errors.New("网站存在运行中的证书任务，请先等待或取消任务")
		}
		if err := tx.Delete(&models.Website{}, "id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.Certificate{}, "website_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.WebsiteSetting{}, "website_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.WebsiteTrafficDaily{}, "website_id = ?", id).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.WebsiteTrafficCursor{}, "website_id = ?", id).Error; err != nil {
			return err
		}
		publisher, publisherErr := service.publisherForSite(&existing)
		if publisherErr != nil {
			return publisherErr
		}
		published, err := publisher.Publish(ctx, map[string]*string{
			configName: nil,
		})
		if err != nil {
			return err
		}
		publication = published
		return nil
	})
	if transactionErr != nil && publication != nil {
		transactionErr = errors.Join(transactionErr, publication.Rollback(context.Background()))
	}
	if transactionErr != nil {
		return errors.Join(transactionErr, restoreStaged())
	}
	for i := range staged {
		if err := os.RemoveAll(staged[i].tombstone); err != nil {
			return fmt.Errorf("remove staged website data: %w", err)
		}
	}
	return nil
}

func validateManagedPath(baseValue, targetValue string) (string, error) {
	return validateManagedPathWithOptions(baseValue, targetValue, false)
}

func validateManagedPathWithOptions(baseValue, targetValue string, allowMissingBase bool) (string, error) {
	base, err := normalizedBasePath(baseValue, "managed root")
	if err != nil {
		return "", err
	}
	target := filepath.Clean(strings.TrimSpace(targetValue))
	if !filepath.IsAbs(target) {
		return "", errors.New("managed path must be absolute")
	}
	relative, err := filepath.Rel(base, target)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("managed path must be strictly below its configured root")
	}
	baseInfo, err := os.Lstat(base)
	if err != nil {
		if allowMissingBase && errors.Is(err, os.ErrNotExist) {
			if err := validateExistingManagedAncestor(base); err != nil {
				return "", err
			}
			return target, nil
		}
		return "", fmt.Errorf("inspect managed root: %w", err)
	}
	if !baseInfo.IsDir() || baseInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("managed root must be a real directory")
	}
	current := base
	parts := strings.Split(relative, string(filepath.Separator))
	for _, part := range parts {
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			break
		}
		if statErr != nil {
			return "", statErr
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("managed path contains a symbolic link")
		}
		if current != target && !info.IsDir() {
			return "", errors.New("managed path contains a non-directory component")
		}
	}
	return target, nil
}

// validateExistingManagedAncestor checks the nearest existing ancestor when a
// preview is opened before the configured managed root has been created. It
// keeps the no-symlink boundary for all existing path components without
// mutating the filesystem.
func validateExistingManagedAncestor(path string) error {
	current := filepath.Clean(path)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("managed root contains a symbolic link")
			}
			if !info.IsDir() {
				return errors.New("managed root ancestor must be a directory")
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect managed root ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func (service *Service) validate() error {
	if service == nil || service.DB == nil {
		return errors.New("website database is not configured")
	}
	if service.Publisher == nil || service.Publisher.Runner == nil {
		return errors.New("website Web server publisher is not configured")
	}
	if _, err := normalizedBasePath(service.WebRoot, "website root"); err != nil {
		return err
	}
	if _, err := normalizedBasePath(service.LogRoot, "website log root"); err != nil {
		return err
	}
	if _, err := normalizedBasePath(service.challengeRoot(), "ACME challenge root"); err != nil {
		return err
	}
	return nil
}

func (service *Service) challengeRoot() string {
	if service == nil || strings.TrimSpace(service.ChallengeRoot) == "" {
		return defaultChallengeRoot
	}
	return service.ChallengeRoot
}

func (service *Service) activeTLSOptions(websiteID int64, normalizedDomains string) (TLSOptions, error) {
	var certificate models.Certificate
	err := service.DB.First(&certificate, "website_id = ?", websiteID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return TLSOptions{}, nil
	}
	if err != nil {
		return TLSOptions{}, err
	}
	if certificate.Status == models.CertificateStatusDisabled {
		return TLSOptions{}, nil
	}
	if !certificateCoversDomains(certificate.Domains, normalizedDomains) {
		return TLSOptions{}, errors.New("新域名不在当前证书范围内，请先关闭 SSL 或重新签发证书")
	}
	options := TLSOptions{
		Enabled:    true,
		ForceHTTPS: certificate.ForceHTTPS,
		CertPath:   certificate.CertificatePath,
		KeyPath:    certificate.PrivateKeyPath,
	}
	if err := service.validateTLSFiles(options); err != nil {
		return TLSOptions{}, err
	}
	return options, nil
}

func certificateCoversDomains(certificateDomains, websiteDomains string) bool {
	available := make(map[string]struct{})
	for _, domain := range strings.Split(certificateDomains, ",") {
		available[strings.ToLower(strings.TrimSpace(domain))] = struct{}{}
	}
	for _, domain := range strings.Split(websiteDomains, ",") {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if _, exists := available[domain]; !exists {
			return false
		}
	}
	return true
}

// CertificateDeployer adapts the website publisher for certificate tasks.
// Every returned rollback restores and reloads the previously active config.
type CertificateDeployer struct {
	service *Service
}

func NewCertificateDeployer() (*CertificateDeployer, error) {
	service, err := defaultService()
	if err != nil {
		return nil, err
	}
	return &CertificateDeployer{service: service}, nil
}

func (deployer *CertificateDeployer) EnsureChallenge(ctx context.Context, websiteID int64) error {
	if deployer == nil || deployer.service == nil {
		return errors.New("certificate deployer is not configured")
	}
	var site models.Website
	if err := deployer.service.DB.First(&site, "id = ?", websiteID).Error; err != nil {
		return err
	}
	if !site.Enabled {
		return errors.New("网站已停用，请先启用网站再申请或续签证书")
	}
	var certificate models.Certificate
	tlsOptions := TLSOptions{}
	err := deployer.service.DB.First(&certificate, "website_id = ?", websiteID).Error
	if err == nil && certificate.Status != models.CertificateStatusDisabled {
		tlsOptions = TLSOptions{
			Enabled:    true,
			ForceHTTPS: certificate.ForceHTTPS,
			CertPath:   certificate.CertificatePath,
			KeyPath:    certificate.PrivateKeyPath,
		}
		if err := deployer.service.validateTLSFiles(tlsOptions); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	_, err = deployer.publish(ctx, websiteID, tlsOptions)
	return err
}

func (deployer *CertificateDeployer) Deploy(
	ctx context.Context,
	websiteID int64,
	certificatePath, privateKeyPath string,
	forceHTTPS bool,
) (func(context.Context) error, error) {
	publication, err := deployer.publish(ctx, websiteID, TLSOptions{
		Enabled:    true,
		ForceHTTPS: forceHTTPS,
		CertPath:   certificatePath,
		KeyPath:    privateKeyPath,
	})
	if err != nil {
		return nil, err
	}
	return publication.Rollback, nil
}

func (deployer *CertificateDeployer) Disable(
	ctx context.Context,
	websiteID int64,
) (func(context.Context) error, error) {
	publication, err := deployer.publish(ctx, websiteID, TLSOptions{})
	if err != nil {
		return nil, err
	}
	return publication.Rollback, nil
}

func (deployer *CertificateDeployer) publish(
	ctx context.Context,
	websiteID int64,
	tlsOptions TLSOptions,
) (*Publication, error) {
	if deployer == nil || deployer.service == nil {
		return nil, errors.New("certificate deployer is not configured")
	}
	if err := deployer.service.validateTLSFiles(tlsOptions); err != nil {
		return nil, err
	}
	var site models.Website
	if err := deployer.service.DB.First(&site, "id = ?", websiteID).Error; err != nil {
		return nil, err
	}
	if !site.Enabled {
		return &Publication{}, nil
	}
	_, settings, err := deployer.service.loadSettings(site.ID)
	if err != nil {
		return nil, err
	}
	previousTLSOptions, err := deployer.service.activeTLSOptions(site.ID, site.Domain)
	if err != nil {
		return nil, err
	}
	previous, err := prepareWebsiteWithTLSAndSettings(
		&site,
		deployer.service.WebRoot,
		deployer.service.LogRoot,
		deployer.service.challengeRoot(),
		previousTLSOptions,
		settings,
	)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(
		&site,
		deployer.service.WebRoot,
		deployer.service.LogRoot,
		deployer.service.challengeRoot(),
		tlsOptions,
		settings,
	)
	if err != nil {
		return nil, err
	}
	content := prepared.config
	content, err = deployer.service.preserveCustomWebsiteConfig(&site, previous.config, content)
	if err != nil {
		return nil, err
	}
	publisher, publisherErr := deployer.service.publisherForSite(&site)
	if publisherErr != nil {
		return nil, publisherErr
	}
	return publisher.Publish(ctx, map[string]*string{
		prepared.configName: &content,
	})
}

func normalizeWebsiteExpiration(site *models.Website, now time.Time) error {
	if site == nil || site.ExpiresAt == nil {
		return nil
	}
	expiresAt := site.ExpiresAt.UTC()
	if site.Enabled && !expiresAt.After(now.UTC()) {
		return errors.New("到期时间必须晚于当前时间")
	}
	site.ExpiresAt = &expiresAt
	return nil
}

func (service *Service) validateTLSFiles(options TLSOptions) error {
	if !options.Enabled {
		return nil
	}
	root := filepath.Clean(strings.TrimSpace(service.CertificateRoot))
	if root == "." || root == string(filepath.Separator) || !filepath.IsAbs(root) {
		root = filepath.Clean(filepath.Join(app.GetBasePath(), "certificates"))
	}
	for label, path := range map[string]string{
		"certificate": options.CertPath,
		"private key": options.KeyPath,
	} {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		relative, err := filepath.Rel(root, cleaned)
		if err != nil || relative == "." || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%s path is outside the managed certificate directory", label)
		}
		info, err := os.Lstat(cleaned)
		if err != nil {
			return fmt.Errorf("read managed %s: %w", label, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("managed %s is not a regular file", label)
		}
		if label == "private key" && info.Mode().Perm()&0077 != 0 {
			return errors.New("managed private key permissions are too broad")
		}
	}
	return nil
}

func (service *Service) ensureWebsiteRoot(siteType, root string) (bool, error) {
	if siteType == "proxy" {
		return false, nil
	}
	// The configured web root may be absent on a fresh installation. Create
	// only that trusted base before the descriptor-relative child validation;
	// validateManagedPath still rejects a symlink or non-directory base.
	if err := os.MkdirAll(filepath.Clean(service.WebRoot), 0755); err != nil {
		return false, fmt.Errorf("create managed website root: %w", err)
	}
	if _, err := validateManagedPath(service.WebRoot, root); err != nil {
		return false, fmt.Errorf("%w: %v", ErrWebsiteRootInvalid, err)
	}
	return ensureManagedDirectory(service.WebRoot, root)
}

func ensureDefaultWebsitePage(site *models.Website) (string, bool, error) {
	if site == nil || site.Type == "proxy" || strings.TrimSpace(site.RootDir) == "" {
		return "", false, nil
	}
	indexPath := filepath.Join(site.RootDir, "index.html")
	file, err := os.OpenFile(indexPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if errors.Is(err, os.ErrExist) {
		return indexPath, false, nil
	}
	if err != nil {
		return indexPath, false, fmt.Errorf("create default website page: %w", err)
	}
	page := defaultWebsitePage(site.Name, site.Domain)
	if _, err := file.WriteString(page); err != nil {
		_ = file.Close()
		_ = os.Remove(indexPath)
		return indexPath, false, fmt.Errorf("write default website page: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(indexPath)
		return indexPath, false, fmt.Errorf("sync default website page: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(indexPath)
		return indexPath, false, fmt.Errorf("close default website page: %w", err)
	}
	return indexPath, true, nil
}

func cleanupWebsiteAddFiles(root, defaultPagePath string, createdRoot, createdDefaultPage bool) {
	if createdDefaultPage && defaultPagePath != "" {
		_ = os.Remove(defaultPagePath)
	}
	if createdRoot && root != "" {
		_ = os.Remove(root)
	}
}

func defaultWebsitePage(name, domains string) string {
	safeName := html.EscapeString(strings.TrimSpace(name))
	safeDomains := html.EscapeString(strings.TrimSpace(domains))
	return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>` + safeName + ` · OneinStack</title>
  <style>
    :root { color-scheme: light; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    * { box-sizing: border-box; }
    body { min-height: 100vh; margin: 0; display: grid; place-items: center; padding: 32px; color: #172033; background: radial-gradient(circle at 20% 0%, #fff1e8 0, transparent 34%), #f6f8fc; }
    main { width: min(720px, 100%); padding: 52px; border: 1px solid #e7eaf0; border-radius: 24px; background: rgba(255,255,255,.92); box-shadow: 0 24px 70px rgba(36,45,70,.10); }
    .brand { display: inline-flex; align-items: center; gap: 12px; color: #ff6b16; font-weight: 750; letter-spacing: .08em; text-transform: uppercase; }
    .mark { display: grid; place-items: center; width: 42px; height: 42px; border-radius: 13px; color: #fff; background: linear-gradient(135deg, #ff8534, #ff5a0a); box-shadow: 0 10px 26px rgba(255,107,22,.28); }
    h1 { margin: 30px 0 14px; font-size: clamp(32px, 7vw, 54px); line-height: 1.08; letter-spacing: -.04em; }
    p { margin: 0; color: #68748a; font-size: 17px; line-height: 1.75; }
    .site { margin-top: 30px; padding: 18px 20px; border: 1px solid #edf0f5; border-radius: 14px; background: #fafbfe; }
    .site strong { display: block; margin-bottom: 5px; color: #20293a; }
    footer { margin-top: 34px; color: #9aa3b3; font-size: 13px; }
  </style>
</head>
<body>
  <main>
    <div class="brand"><span class="mark">1S</span><span>OneinStack Panel</span></div>
    <h1>网站已经准备就绪</h1>
    <p>站点配置已成功发布。请上传您的项目文件，或在面板的文件管理中替换此默认页面。</p>
    <div class="site"><strong>` + safeName + `</strong><span>` + safeDomains + `</span></div>
    <footer>Powered by OneinStack</footer>
  </main>
</body>
</html>
`
}

func Check() bool {
	server, err := DetectWebServer()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(
		ctx,
		server.BinaryPath,
		"-t",
		"-p",
		ensureTrailingSeparator(server.Prefix),
		"-c",
		server.MainConfigPath,
	).Run() == nil
}
