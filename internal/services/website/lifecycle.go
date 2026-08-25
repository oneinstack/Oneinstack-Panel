package website

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"oneinstack/internal/models"
	runtimelog "oneinstack/internal/services/log"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	WebsiteDisabledManual  = "manual"
	WebsiteDisabledExpired = "expired"
	maxTrafficReadBytes    = 16 << 20
)

var nginxTrafficLinePattern = regexp.MustCompile(
	`\[(\d{2}/[A-Za-z]{3}/\d{4}):[^\]]+\]\s+"[^"]*"\s+\d{3}\s+(\d+|-)`,
)

// SetEnabled safely removes or restores one managed virtual-host file and
// reloads the detected Nginx/OpenResty service. Website data is never removed.
func (service *Service) SetEnabled(ctx context.Context, id int64, enabled bool) (*models.Website, error) {
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
	if site.Enabled == enabled {
		return &site, nil
	}
	now := time.Now()
	if enabled && site.ExpiresAt != nil && !site.ExpiresAt.After(now) {
		return nil, fmt.Errorf("%w: 网站已到期，请先修改到期时间后再启用", ErrWebsiteExpired)
	}

	configName := strings.TrimSpace(site.Name) + ".conf"
	if !configNamePattern.MatchString(configName) {
		return nil, errors.New("stored website has an unsafe Nginx config name")
	}
	changes := map[string]*string{configName: nil}
	if enabled {
		_, settings, err := service.loadSettings(site.ID)
		if err != nil {
			return nil, err
		}
		tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
		if err != nil {
			return nil, err
		}
		prepared, err := prepareWebsiteWithTLSAndSettings(
			&site,
			service.WebRoot,
			service.LogRoot,
			service.challengeRoot(),
			tlsOptions,
			settings,
		)
		if err != nil {
			return nil, wrapWebsiteParameterError(err)
		}
		content := prepared.config
		changes[configName] = &content
	}

	var publication *Publication
	err := service.DB.Transaction(func(tx *gorm.DB) error {
		publisher, publisherErr := service.publisherForSite(&site)
		if publisherErr != nil {
			return publisherErr
		}
		published, publishErr := publisher.Publish(ctx, changes)
		if publishErr != nil {
			return publishErr
		}
		publication = published
		reason := ""
		if !enabled {
			reason = WebsiteDisabledManual
		}
		if updateErr := tx.Model(&models.Website{}).
			Where("id = ?", id).
			Updates(map[string]any{
				"enabled":         enabled,
				"disabled_reason": reason,
				"update_time":     now,
			}).Error; updateErr != nil {
			return updateErr
		}
		return nil
	})
	if err != nil {
		if publication != nil {
			err = errors.Join(err, publication.Rollback(context.Background()))
		}
		return nil, err
	}
	if err := service.DB.First(&site, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &site, nil
}

func (service *Service) disableExpired(ctx context.Context, now time.Time) error {
	var sites []models.Website
	if err := service.DB.
		Where("enabled = ? AND expires_at IS NOT NULL AND expires_at <= ?", true, now).
		Find(&sites).Error; err != nil {
		return err
	}
	var result error
	for i := range sites {
		if _, err := service.SetEnabled(ctx, sites[i].ID, false); err != nil {
			result = errors.Join(result, fmt.Errorf("disable expired website %s: %w", sites[i].Name, err))
			continue
		}
		if err := service.DB.Model(&models.Website{}).
			Where("id = ?", sites[i].ID).
			Update("disabled_reason", WebsiteDisabledExpired).Error; err != nil {
			result = errors.Join(result, fmt.Errorf("record expiration for %s: %w", sites[i].Name, err))
		}
	}
	return result
}

func (service *Service) collectTraffic() error {
	if service == nil || service.DB == nil {
		return errors.New("website database is not configured")
	}
	logRoot := filepath.Clean(strings.TrimSpace(service.LogRoot))
	if !filepath.IsAbs(logRoot) || logRoot == string(filepath.Separator) {
		return errors.New("website log root must be a non-root absolute path")
	}
	var sites []models.Website
	if err := service.DB.Select("id", "name").Find(&sites).Error; err != nil {
		return err
	}
	var result error
	for i := range sites {
		if err := service.collectSiteTraffic(&sites[i], logRoot); err != nil {
			result = errors.Join(result, fmt.Errorf("collect traffic for %s: %w", sites[i].Name, err))
		}
	}
	return result
}

func (service *Service) collectSiteTraffic(site *models.Website, logRoot string) error {
	if site == nil || site.ID <= 0 {
		return errors.New("website is required")
	}
	if !configNamePattern.MatchString(strings.TrimSpace(site.Name) + ".conf") {
		return errors.New("stored website has an unsafe log name")
	}
	logName := strings.ReplaceAll(strings.TrimSpace(site.Name), ".", "_") + "_access.log"
	logPath := filepath.Join(logRoot, logName)
	relative, err := filepath.Rel(logRoot, logPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("website access log escapes the configured log root")
	}
	file, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("website access log is not a regular file")
	}
	identity := trafficFileIdentity(info)
	var cursor models.WebsiteTrafficCursor
	err = service.DB.First(&cursor, "website_id = ?", site.ID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if errors.Is(err, gorm.ErrRecordNotFound) || cursor.LogPath != logPath ||
		cursor.FileIdentity != identity || cursor.Offset > info.Size() {
		cursor = models.WebsiteTrafficCursor{
			WebsiteID:    site.ID,
			LogPath:      logPath,
			FileIdentity: identity,
			Offset:       0,
		}
	}
	if cursor.Offset == info.Size() {
		return nil
	}
	if _, err := file.Seek(cursor.Offset, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTrafficReadBytes))
	if err != nil {
		return err
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return nil
	}
	complete := data[:lastNewline+1]
	aggregates := parseTrafficLines(complete)
	newOffset := cursor.Offset + int64(lastNewline+1)
	return service.DB.Transaction(func(tx *gorm.DB) error {
		for day, aggregate := range aggregates {
			record := models.WebsiteTrafficDaily{
				WebsiteID:    site.ID,
				Day:          day,
				BytesSent:    aggregate.bytes,
				RequestCount: aggregate.requests,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "website_id"}, {Name: "day"}},
				DoUpdates: clause.Assignments(map[string]any{
					"bytes_sent":    gorm.Expr("website_traffic_daily.bytes_sent + excluded.bytes_sent"),
					"request_count": gorm.Expr("website_traffic_daily.request_count + excluded.request_count"),
					"updated_at":    time.Now(),
				}),
			}).Create(&record).Error; err != nil {
				return err
			}
		}
		cursor.Offset = newOffset
		cursor.LogPath = logPath
		cursor.FileIdentity = identity
		return tx.Save(&cursor).Error
	})
}

type trafficAggregate struct {
	bytes    int64
	requests int64
}

func parseTrafficLines(data []byte) map[string]trafficAggregate {
	result := make(map[string]trafficAggregate)
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		matches := nginxTrafficLinePattern.FindSubmatch(line)
		if len(matches) != 3 {
			continue
		}
		parsedDay, err := time.Parse("02/Jan/2006", string(matches[1]))
		if err != nil {
			continue
		}
		bytesSent := int64(0)
		if string(matches[2]) != "-" {
			bytesSent, err = strconv.ParseInt(string(matches[2]), 10, 64)
			if err != nil || bytesSent < 0 {
				continue
			}
		}
		day := parsedDay.Format("2006-01-02")
		aggregate := result[day]
		aggregate.bytes += bytesSent
		aggregate.requests++
		result[day] = aggregate
	}
	return result
}

func trafficFileIdentity(info os.FileInfo) string {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino)
	}
	return fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
}

type LifecycleManager struct {
	service  *Service
	interval time.Duration
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
	alerted  map[int64]string
}

func NewLifecycleManager(service *Service, interval time.Duration) (*LifecycleManager, error) {
	if service == nil {
		return nil, errors.New("website service is required")
	}
	if err := service.validate(); err != nil {
		return nil, err
	}
	if interval <= 0 {
		return nil, errors.New("website lifecycle interval must be positive")
	}
	return &LifecycleManager{service: service, interval: interval, alerted: make(map[int64]string)}, nil
}

func NewDefaultLifecycleManager(interval time.Duration) (*LifecycleManager, error) {
	service, err := defaultService()
	if err != nil {
		return nil, err
	}
	return NewLifecycleManager(service, interval)
}

func (manager *LifecycleManager) Start() {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.cancel = cancel
	manager.done = make(chan struct{})
	go manager.run(ctx, manager.done)
}

func (manager *LifecycleManager) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	manager.runOnce(ctx)
	ticker := time.NewTicker(manager.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			manager.runOnce(ctx)
		}
	}
}

func (manager *LifecycleManager) runOnce(ctx context.Context) error {
	trafficErr := manager.service.collectTraffic()
	expirationErr := manager.service.disableExpired(ctx, time.Now())
	_, restoreErr := manager.service.RestoreMissingManagedConfigs(ctx)
	tamperErr := manager.service.enforceTamperProtection(ctx)
	alertErr := manager.emitTrafficAlerts()
	return errors.Join(trafficErr, expirationErr, restoreErr, tamperErr, alertErr)
}

func (service *Service) enforceTamperProtection(ctx context.Context) error {
	var settings []models.WebsiteSetting
	if err := service.DB.Where("tamper_protection = ?", true).Find(&settings).Error; err != nil {
		return err
	}
	var result error
	for i := range settings {
		var site models.Website
		if err := service.DB.First(&site, "id = ? AND enabled = ?", settings[i].WebsiteID, true).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			result = errors.Join(result, err)
			continue
		}
		tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		prepared, err := prepareWebsiteWithTLSAndSettings(
			&site, service.WebRoot, service.LogRoot, service.challengeRoot(), tlsOptions, &settings[i],
		)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		configPath, err := service.ConfigFile(&site)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		actual, readErr := os.ReadFile(configPath)
		if readErr == nil && string(actual) == prepared.config {
			continue
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			result = errors.Join(result, readErr)
			continue
		}
		content := prepared.config
		publisher, publisherErr := service.publisherForSite(&site)
		if publisherErr != nil {
			result = errors.Join(result, publisherErr)
			continue
		}
		if _, err := publisher.Publish(ctx, map[string]*string{prepared.configName: &content}); err != nil {
			result = errors.Join(result, fmt.Errorf("restore protected website %s: %w", site.Name, err))
			continue
		}
		if manager := runtimelog.RuntimeDefault(); manager != nil {
			manager.Enqueue(runtimelog.LevelWarning, "website", "网站 "+site.Name+" 的运行配置被外部修改，已自动恢复可信版本")
		}
	}
	return result
}

func (manager *LifecycleManager) emitTrafficAlerts() error {
	day := time.Now().Format("2006-01-02")
	type alertRecord struct {
		WebsiteID  int64
		Name       string
		BytesSent  int64
		AlertBytes int64
	}
	var records []alertRecord
	err := manager.service.DB.Table("website_setting AS settings").
		Select("settings.website_id, websites.name, traffic.bytes_sent, settings.traffic_alert_bytes AS alert_bytes").
		Joins("JOIN websites ON websites.id = settings.website_id").
		Joins("JOIN website_traffic_daily AS traffic ON traffic.website_id = settings.website_id AND traffic.day = ?", day).
		Where("settings.traffic_alert = ? AND settings.traffic_alert_bytes > 0 AND traffic.bytes_sent >= settings.traffic_alert_bytes", true).
		Scan(&records).Error
	if err != nil {
		return err
	}
	logger := runtimelog.RuntimeDefault()
	if logger == nil {
		return nil
	}
	for _, record := range records {
		if manager.alerted[record.WebsiteID] == day {
			continue
		}
		manager.alerted[record.WebsiteID] = day
		logger.Enqueue(
			runtimelog.LevelWarning,
			"website",
			fmt.Sprintf("网站 %s 今日流量已达到告警阈值：%d / %d 字节", record.Name, record.BytesSent, record.AlertBytes),
		)
	}
	return nil
}

func (manager *LifecycleManager) Stop(ctx context.Context) error {
	manager.mu.Lock()
	cancel := manager.cancel
	done := manager.done
	manager.cancel = nil
	manager.done = nil
	manager.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
