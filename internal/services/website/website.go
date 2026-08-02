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
	return &Service{
		DB:              app.DB(),
		WebRoot:         app.ONE_CONFIG.System.WebPath,
		LogRoot:         app.ONE_CONFIG.System.LogPath,
		ChallengeRoot:   app.ONE_CONFIG.System.ACMEChallengePath,
		CertificateRoot: app.ONE_CONFIG.System.CertificatePath,
		Publisher: &Publisher{
			ConfigDir:   server.SiteConfigDir,
			NginxBinary: server.BinaryPath,
			Runner:      OSCommandRunner{},
		},
		ConfigManager: newWebServerConfigManager(server),
		Firewall:      safeservice.NewDefaultService(),
	}, nil
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
		return nil, errors.New("website ID is required")
	}
	var site models.Website
	if err := service.DB.First(&site, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if _, err := service.ManagedRoot(&site); err != nil {
		return nil, err
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
		return "", errors.New("stored website has an unsafe Nginx config name")
	}
	configRoot, err := normalizedBasePath(service.Publisher.ConfigDir, "Nginx config root")
	if err != nil {
		return "", err
	}
	return filepath.Join(configRoot, name), nil
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
	prepared, err := prepareWebsiteWithTLS(
		param,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		TLSOptions{},
	)
	if err != nil {
		return err
	}
	createdRoot, err := ensureWebsiteRoot(prepared.model.Type, prepared.model.RootDir)
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
			return fmt.Errorf("网站 %s 已存在", prepared.model.Name)
		}
		if err := tx.Create(&prepared.model).Error; err != nil {
			return err
		}
		content := prepared.config
		published, err := service.Publisher.Publish(ctx, map[string]*string{
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
		return errors.New("website ID is required")
	}
	var existing models.Website
	if err := service.DB.First(&existing, "id = ?", param.ID).Error; err != nil {
		return err
	}
	_, settings, err := service.loadSettings(existing.ID)
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
		return err
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
			return err
		}
		prepared.model.ID = existing.ID
		prepared.model.CreateTime = existing.CreateTime
		prepared.model.Enabled = existing.Enabled
		prepared.model.DisabledReason = existing.DisabledReason
		if err := normalizeWebsiteExpiration(&prepared.model, time.Now()); err != nil {
			return err
		}
	}
	createdRoot, err := ensureWebsiteRoot(prepared.model.Type, prepared.model.RootDir)
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

	var publication *Publication
	transactionErr := service.DB.Transaction(func(tx *gorm.DB) error {
		var duplicate int64
		if err := tx.Model(&models.Website{}).
			Where("id <> ? AND (name = ? OR domain = ?)", existing.ID, prepared.model.Name, prepared.model.Domain).
			Count(&duplicate).Error; err != nil {
			return err
		}
		if duplicate > 0 {
			return fmt.Errorf("网站 %s 已存在", prepared.model.Name)
		}
		if err := tx.Save(&prepared.model).Error; err != nil {
			return err
		}
		changes := map[string]*string{prepared.configName: nil}
		if prepared.model.Enabled {
			content := prepared.config
			changes[prepared.configName] = &content
		}
		if oldName != prepared.configName {
			changes[oldName] = nil
		}
		published, err := service.Publisher.Publish(ctx, changes)
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
		return errors.New("website ID is required")
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
		published, err := service.Publisher.Publish(ctx, map[string]*string{
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

func (service *Service) validate() error {
	if service == nil || service.DB == nil {
		return errors.New("website database is not configured")
	}
	if service.Publisher == nil || service.Publisher.Runner == nil {
		return errors.New("website Nginx publisher is not configured")
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
	return deployer.service.Publisher.Publish(ctx, map[string]*string{
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

func ensureWebsiteRoot(siteType, root string) (bool, error) {
	if siteType == "proxy" {
		return false, nil
	}
	info, err := os.Stat(root)
	if err == nil {
		if !info.IsDir() {
			return false, errors.New("website root exists but is not a directory")
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("stat website root: %w", err)
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return false, fmt.Errorf("create website root: %w", err)
	}
	return true, nil
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
