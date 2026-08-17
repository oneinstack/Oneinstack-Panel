package website

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type WebsiteDirectoryBinding struct {
	Path      string `json:"path"`
	Directory string `json:"directory"`
	Enabled   bool   `json:"enabled"`
}

type WebsiteRedirectRule struct {
	Source  string `json:"source"`
	Target  string `json:"target"`
	Status  int    `json:"status"`
	Enabled bool   `json:"enabled"`
}

type WebsiteProxyRule struct {
	Path    string `json:"path"`
	Target  string `json:"target"`
	Host    string `json:"host"`
	Enabled bool   `json:"enabled"`
}

type WebsiteSettings struct {
	RunningDirectory  string                    `json:"running_directory"`
	DirectoryListing  bool                      `json:"directory_listing"`
	DefaultDocuments  string                    `json:"default_documents"`
	AllowedIPs        string                    `json:"allowed_ips"`
	DeniedIPs         string                    `json:"denied_ips"`
	RateLimitKB       int64                     `json:"rate_limit_kb"`
	RateLimitAfterKB  int64                     `json:"rate_limit_after_kb"`
	RewriteRules      string                    `json:"rewrite_rules"`
	Bindings          []WebsiteDirectoryBinding `json:"bindings"`
	Redirects         []WebsiteRedirectRule     `json:"redirects"`
	ProxyRules        []WebsiteProxyRule        `json:"proxy_rules"`
	HotlinkEnabled    bool                      `json:"hotlink_enabled"`
	HotlinkAllowEmpty bool                      `json:"hotlink_allow_empty"`
	HotlinkDomains    string                    `json:"hotlink_domains"`
	HotlinkExtensions string                    `json:"hotlink_extensions"`
	SecurityHeaders   bool                      `json:"security_headers"`
	DeniedPaths       string                    `json:"denied_paths"`
	PHPBackend        string                    `json:"php_backend"`
	TamperProtection  bool                      `json:"tamper_protection"`
	TrafficAlert      bool                      `json:"traffic_alert"`
	TrafficAlertBytes int64                     `json:"traffic_alert_bytes"`
	AccessLogEnabled  bool                      `json:"access_log_enabled"`
	ErrorLogEnabled   bool                      `json:"error_log_enabled"`
	UpdatedAt         time.Time                 `json:"updated_at,omitempty"`
}

type WebsiteSettingsDocument struct {
	Website  models.Website  `json:"website"`
	Settings WebsiteSettings `json:"settings"`
}

type WebsiteLogDocument struct {
	Type    string `json:"type"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Lines   int    `json:"lines"`
}

type renderedWebsiteSettings struct {
	RootDir           string
	DefaultDocuments  string
	AutoIndex         string
	PHPBackend        string
	RewriteDirectives string
	ServerDirectives  string
	ExtraLocations    string
	AccessLogEnabled  bool
	ErrorLogEnabled   bool
}

func defaultWebsiteSettings() WebsiteSettings {
	return WebsiteSettings{
		DefaultDocuments:  "index.php index.html index.htm",
		HotlinkAllowEmpty: true,
		HotlinkExtensions: "jpg jpeg png gif webp svg css js mp4 mp3",
		SecurityHeaders:   true,
		PHPBackend:        "unix:/dev/shm/php-cgi.sock",
		AccessLogEnabled:  true,
		ErrorLogEnabled:   true,
	}
}

func (service *Service) GetSettings(id int64) (*WebsiteSettingsDocument, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	site, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	settings, _, err := service.loadSettings(id)
	if err != nil {
		return nil, err
	}
	return &WebsiteSettingsDocument{Website: *site, Settings: settings}, nil
}

func (service *Service) UpdateSettings(
	ctx context.Context,
	id int64,
	settings WebsiteSettings,
) (*WebsiteSettingsDocument, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	site, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	_, previousRecord, err := service.loadSettings(id)
	if err != nil {
		return nil, err
	}
	settings.UpdatedAt = time.Now()
	record, err := settings.toModel(id)
	if err != nil {
		return nil, err
	}
	tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
	if err != nil {
		return nil, err
	}
	previous, err := prepareWebsiteWithTLSAndSettings(
		site,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		tlsOptions,
		previousRecord,
	)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(
		site,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		tlsOptions,
		record,
	)
	if err != nil {
		return nil, err
	}

	var publication *Publication
	err = service.DB.Transaction(func(tx *gorm.DB) error {
		if saveErr := tx.Save(record).Error; saveErr != nil {
			return saveErr
		}
		if !site.Enabled {
			return nil
		}
		content := prepared.config
		if site.Enabled {
			configPath, pathErr := service.ConfigFile(site)
			if pathErr != nil {
				return pathErr
			}
			current, readErr := readBoundedFile(configPath, maxWebServerConfigBytes)
			if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
				return readErr
			}
			if readErr == nil {
				content = mergeWebsiteSettingsConfig(previous.config, prepared.config, string(current))
			}
		}
		published, publishErr := service.Publisher.Publish(ctx, map[string]*string{
			prepared.configName: &content,
		})
		if publishErr != nil {
			return publishErr
		}
		publication = published
		return nil
	})
	if err != nil {
		if publication != nil {
			err = errors.Join(err, publication.Rollback(context.Background()))
		}
		return nil, err
	}
	return &WebsiteSettingsDocument{Website: *site, Settings: settings}, nil
}

// mergeWebsiteSettingsConfig applies the structured-settings render while
// retaining lines added manually to the active configuration. It deliberately
// uses the previous generated config as the base: fields controlled by website
// settings still change, while additions such as comments, directives and
// location blocks remain at their original relative position.
func mergeWebsiteSettingsConfig(previous, rendered, current string) string {
	baseLines := configLines(previous)
	renderedLines := configLines(rendered)
	currentLines := configLines(current)
	baseToCurrent := lcsLineMatches(baseLines, currentLines)
	baseToRendered := lcsLineMatches(baseLines, renderedLines)
	if len(baseToCurrent) == len(baseLines) && len(currentLines) == len(baseLines) {
		return rendered
	}

	insertAfter := make(map[int][][]string)
	currentStart := 0
	previousBase := -1
	for baseIndex := 0; baseIndex <= len(baseLines); baseIndex++ {
		currentIndex, matched := baseToCurrent[baseIndex]
		if baseIndex == len(baseLines) {
			currentIndex = len(currentLines)
			matched = true
		}
		if !matched {
			continue
		}
		if currentStart < currentIndex {
			manual := filterConflictingManualConfig(
				currentLines[currentStart:currentIndex], renderedLines,
			)
			if len(manual) == 0 {
				if baseIndex < len(baseLines) {
					previousBase = baseIndex
					currentStart = currentIndex + 1
				}
				continue
			}
			if renderedIndex, ok := baseToRendered[previousBase]; ok {
				insertAfter[renderedIndex] = append(insertAfter[renderedIndex], manual)
			} else {
				insertAfter[-1] = append(insertAfter[-1], manual)
			}
		}
		if baseIndex < len(baseLines) {
			previousBase = baseIndex
			currentStart = currentIndex + 1
		}
	}

	merged := make([]string, 0, len(renderedLines)+len(currentLines)-len(baseLines))
	for _, block := range insertAfter[-1] {
		merged = append(merged, block...)
	}
	for index, line := range renderedLines {
		merged = append(merged, line)
		for _, block := range insertAfter[index] {
			merged = append(merged, block...)
		}
	}
	return strings.Join(merged, "\n") + "\n"
}

// filterConflictingManualConfig keeps manual additions unless the regenerated
// configuration now owns the same directive or location. This lets a
// structured setting take precedence over a conflicting manual edit while
// preserving unrelated, user-managed blocks.
func filterConflictingManualConfig(manual, rendered []string) []string {
	renderedLocations := make(map[string]struct{})
	renderedDirectives := make(map[string]struct{})
	for _, line := range rendered {
		if key, ok := configLocationKey(line); ok {
			renderedLocations[key] = struct{}{}
		}
		if key, ok := managedDirectiveKey(line); ok {
			renderedDirectives[key] = struct{}{}
		}
	}

	kept := make([]string, 0, len(manual))
	for index := 0; index < len(manual); {
		if key, ok := configLocationKey(manual[index]); ok {
			block, next := configBlock(manual, index)
			if _, conflict := renderedLocations[key]; !conflict {
				kept = append(kept, block...)
			}
			index = next
			continue
		}
		if key, ok := managedDirectiveKey(manual[index]); ok {
			if _, conflict := renderedDirectives[key]; conflict {
				index++
				continue
			}
		}
		kept = append(kept, manual[index])
		index++
	}
	return kept
}

func configLocationKey(line string) (string, bool) {
	line = strings.TrimSpace(line)
	openBrace := strings.Index(line, "{")
	if openBrace < 0 {
		return "", false
	}
	fields := strings.Fields(strings.TrimSpace(line[:openBrace]))
	if len(fields) < 2 || fields[0] != "location" {
		return "", false
	}
	pathIndex := 1
	if fields[pathIndex] == "=" || fields[pathIndex] == "^~" ||
		fields[pathIndex] == "~" || fields[pathIndex] == "~*" {
		pathIndex++
	}
	if pathIndex >= len(fields) {
		return "", false
	}
	path := fields[pathIndex]
	if strings.HasPrefix(path, "/") {
		path = strings.TrimSuffix(path, "/")
		if path == "" {
			path = "/"
		}
	}
	return path, true
}

func configBlock(lines []string, start int) ([]string, int) {
	depth := 0
	for index := start; index < len(lines); index++ {
		depth += strings.Count(lines[index], "{") - strings.Count(lines[index], "}")
		if depth == 0 {
			return lines[start : index+1], index + 1
		}
	}
	return lines[start:], len(lines)
}

func managedDirectiveKey(line string) (string, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) == 0 {
		return "", false
	}
	switch fields[0] {
	case "root", "index", "autoindex", "access_log", "error_log", "limit_rate", "limit_rate_after", "try_files", "fastcgi_pass", "fastcgi_index", "include", "fastcgi_param":
		return fields[0], true
	case "add_header":
		if len(fields) > 1 {
			return fields[0] + " " + fields[1], true
		}
	}
	return "", false
}

// preserveCustomWebsiteConfig reapplies manually added configuration fragments
// while a managed website configuration is being regenerated. The caller must
// provide the configuration generated from the website's state before its
// change; that makes managed changes authoritative without discarding custom
// directives in the active file.
func (service *Service) preserveCustomWebsiteConfig(
	site *models.Website,
	previous, rendered string,
) (string, error) {
	configPath, err := service.ConfigFile(site)
	if err != nil {
		return "", err
	}
	current, err := readBoundedFile(configPath, maxWebServerConfigBytes)
	if errors.Is(err, os.ErrNotExist) {
		return rendered, nil
	}
	if err != nil {
		return "", err
	}
	return mergeWebsiteSettingsConfig(previous, rendered, string(current)), nil
}

func configLines(content string) []string {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return nil
	}
	return strings.Split(content, "\n")
}

// lcsLineMatches maps each line index in base to its identical line in other.
// Website configurations are intentionally bounded, making this predictable
// O(n*m) merge safe and easier to audit than a heuristic text replacement.
func lcsLineMatches(base, other []string) map[int]int {
	dp := make([][]int, len(base)+1)
	for i := range dp {
		dp[i] = make([]int, len(other)+1)
	}
	for i := len(base) - 1; i >= 0; i-- {
		for j := len(other) - 1; j >= 0; j-- {
			if base[i] == other[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	matches := make(map[int]int)
	for i, j := 0, 0; i < len(base) && j < len(other); {
		if base[i] == other[j] {
			matches[i] = j
			i++
			j++
		} else if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return matches
}

func (service *Service) ReadLog(id int64, logType string, lineLimit int) (*WebsiteLogDocument, error) {
	if err := service.validate(); err != nil {
		return nil, err
	}
	site, err := service.Get(id)
	if err != nil {
		return nil, err
	}
	logType = strings.ToLower(strings.TrimSpace(logType))
	if logType != "access" && logType != "error" {
		return nil, errors.New("日志类型只能是 access 或 error")
	}
	if lineLimit <= 0 {
		lineLimit = 200
	}
	if lineLimit > 2000 {
		lineLimit = 2000
	}
	logRoot := filepath.Clean(strings.TrimSpace(service.LogRoot))
	logName := strings.ReplaceAll(site.Name, ".", "_") + "_" + logType + ".log"
	logPath := filepath.Join(logRoot, logName)
	relative, err := filepath.Rel(logRoot, logPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("网站日志路径不安全")
	}
	file, err := os.Open(logPath)
	if errors.Is(err, os.ErrNotExist) {
		return &WebsiteLogDocument{Type: logType, Path: logPath}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("网站日志不是普通文件")
	}
	const maxTailBytes int64 = 4 << 20
	start := info.Size() - maxTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	return &WebsiteLogDocument{
		Type: logType, Path: logPath, Content: strings.Join(lines, "\n"), Lines: len(lines),
	}, nil
}

func (service *Service) ReadManagedConfig(
	ctx context.Context,
	id int64,
) (WebServerConfigDocument, error) {
	site, err := service.Get(id)
	if err != nil {
		return WebServerConfigDocument{}, err
	}
	manager, err := service.managedConfigManager()
	if err != nil {
		return WebServerConfigDocument{}, err
	}
	relative, err := service.managedConfigRelativePath(manager, site)
	if err != nil {
		return WebServerConfigDocument{}, err
	}
	document, err := manager.Read(relative)
	if err == nil {
		return document, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return WebServerConfigDocument{}, err
	}
	if !site.Enabled {
		return WebServerConfigDocument{}, errors.New("网站已停用，当前没有运行配置文件")
	}
	if err := service.restoreManagedConfig(ctx, site); err != nil {
		return WebServerConfigDocument{}, fmt.Errorf("恢复缺失的网站配置: %w", err)
	}
	return manager.Read(relative)
}

// RestoreMissingManagedConfigs rebuilds every missing enabled website virtual
// host from the Panel database. The database is the canonical source for
// managed website settings; existing files are deliberately left untouched so
// manual edits are not overwritten during a component reinstall.
func (service *Service) RestoreMissingManagedConfigs(ctx context.Context) (int, error) {
	if err := service.validate(); err != nil {
		return 0, err
	}
	binary := filepath.Clean(strings.TrimSpace(service.Publisher.NginxBinary))
	if filepath.IsAbs(binary) {
		info, err := os.Stat(binary)
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		if err != nil {
			return 0, fmt.Errorf("检查 Web 服务器程序: %w", err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return 0, errors.New("Web 服务器程序不可执行")
		}
	}
	var sites []models.Website
	if err := service.DB.
		Where("enabled = ?", true).
		Order("id ASC").
		Find(&sites).Error; err != nil {
		return 0, fmt.Errorf("读取启用的网站列表: %w", err)
	}
	changes := make(map[string]*string)
	for i := range sites {
		site := &sites[i]
		configPath, err := service.ConfigFile(site)
		if err != nil {
			return 0, fmt.Errorf("检查网站 %s 的配置路径: %w", site.Name, err)
		}
		info, statErr := os.Lstat(configPath)
		switch {
		case statErr == nil:
			if !info.Mode().IsRegular() {
				return 0, fmt.Errorf("网站 %s 的配置路径不是普通文件", site.Name)
			}
			continue
		case !errors.Is(statErr, os.ErrNotExist):
			return 0, fmt.Errorf("检查网站 %s 的配置文件: %w", site.Name, statErr)
		}

		_, settings, err := service.loadSettings(site.ID)
		if err != nil {
			return 0, fmt.Errorf("读取网站 %s 的配置参数: %w", site.Name, err)
		}
		tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
		if err != nil {
			return 0, fmt.Errorf("读取网站 %s 的证书配置: %w", site.Name, err)
		}
		prepared, err := prepareWebsiteWithTLSAndSettings(
			site,
			service.WebRoot,
			service.LogRoot,
			service.challengeRoot(),
			tlsOptions,
			settings,
		)
		if err != nil {
			return 0, fmt.Errorf("重新生成网站 %s 的配置: %w", site.Name, err)
		}
		content := prepared.config
		changes[prepared.configName] = &content
	}
	if len(changes) == 0 {
		return 0, nil
	}
	if _, err := service.Publisher.Publish(ctx, changes); err != nil {
		return 0, fmt.Errorf("发布恢复的网站配置: %w", err)
	}
	return len(changes), nil
}

func (service *Service) UpdateManagedConfig(
	ctx context.Context,
	id int64,
	content, revision string,
) (WebServerConfigUpdateResult, error) {
	site, err := service.Get(id)
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	if !site.Enabled {
		return WebServerConfigUpdateResult{}, errors.New("网站已停用，请先启用网站再编辑运行配置")
	}
	manager, err := service.managedConfigManager()
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	relative, err := service.managedConfigRelativePath(manager, site)
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	return manager.Update(ctx, WebServerConfigUpdate{
		Path: relative, Content: content, Revision: revision,
	})
}

func (service *Service) managedConfigManager() (*WebServerConfigManager, error) {
	if service != nil && service.ConfigManager != nil {
		return service.ConfigManager, nil
	}
	return NewDefaultWebServerConfigManager()
}

func (service *Service) managedConfigRelativePath(
	manager *WebServerConfigManager,
	site *models.Website,
) (string, error) {
	if manager == nil {
		return "", errors.New("Web 服务器配置管理器未初始化")
	}
	configPath, err := service.ConfigFile(site)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(manager.Server.ConfigRoot, configPath)
	if err != nil {
		return "", err
	}
	normalized := filepath.ToSlash(relative)
	if normalized == ".." || strings.HasPrefix(normalized, "../") {
		return "", fmt.Errorf(
			"网站配置目录 %s 不属于当前 %s 配置根目录 %s",
			filepath.Dir(configPath), manager.Server.Name, manager.Server.ConfigRoot,
		)
	}
	return normalized, nil
}

// restoreManagedConfig repairs an enabled website whose runtime configuration
// was removed outside Panel. The database remains the source of truth; the
// regenerated configuration still passes the normal syntax test and reload
// transaction before it becomes active.
func (service *Service) restoreManagedConfig(ctx context.Context, site *models.Website) error {
	settings, record, err := service.loadSettings(site.ID)
	if err != nil {
		return err
	}
	if record == nil {
		record, err = settings.toModel(site.ID)
		if err != nil {
			return err
		}
	}
	tlsOptions, err := service.activeTLSOptions(site.ID, site.Domain)
	if err != nil {
		return err
	}
	prepared, err := prepareWebsiteWithTLSAndSettings(
		site,
		service.WebRoot,
		service.LogRoot,
		service.challengeRoot(),
		tlsOptions,
		record,
	)
	if err != nil {
		return err
	}
	content := prepared.config
	_, err = service.Publisher.Publish(ctx, map[string]*string{
		prepared.configName: &content,
	})
	return err
}

func (service *Service) loadSettings(id int64) (WebsiteSettings, *models.WebsiteSetting, error) {
	var record models.WebsiteSetting
	err := service.DB.First(&record, "website_id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		defaults := defaultWebsiteSettings()
		model, modelErr := defaults.toModel(id)
		return defaults, model, modelErr
	}
	if err != nil {
		return WebsiteSettings{}, nil, err
	}
	settings, err := websiteSettingsFromModel(&record)
	return settings, &record, err
}

func websiteSettingsFromModel(record *models.WebsiteSetting) (WebsiteSettings, error) {
	settings := WebsiteSettings{
		RunningDirectory: record.RunningDirectory, DirectoryListing: record.DirectoryListing,
		DefaultDocuments: record.DefaultDocuments, AllowedIPs: record.AllowedIPs,
		DeniedIPs: record.DeniedIPs, RateLimitKB: record.RateLimitKB,
		RateLimitAfterKB: record.RateLimitAfterKB, RewriteRules: record.RewriteRules,
		HotlinkEnabled: record.HotlinkEnabled, HotlinkAllowEmpty: record.HotlinkAllowEmpty,
		HotlinkDomains: record.HotlinkDomains, HotlinkExtensions: record.HotlinkExtensions,
		SecurityHeaders: record.SecurityHeaders, DeniedPaths: record.DeniedPaths,
		PHPBackend: record.PHPBackend, TamperProtection: record.TamperProtection,
		TrafficAlert: record.TrafficAlert, TrafficAlertBytes: record.TrafficAlertBytes,
		AccessLogEnabled: record.AccessLogEnabled, ErrorLogEnabled: record.ErrorLogEnabled,
		UpdatedAt: record.UpdatedAt,
	}
	if strings.TrimSpace(settings.DefaultDocuments) == "" {
		settings.DefaultDocuments = defaultWebsiteSettings().DefaultDocuments
	}
	if strings.TrimSpace(settings.PHPBackend) == "" {
		settings.PHPBackend = defaultWebsiteSettings().PHPBackend
	}
	if strings.TrimSpace(settings.HotlinkExtensions) == "" {
		settings.HotlinkExtensions = defaultWebsiteSettings().HotlinkExtensions
	}
	if record.BindingsJSON != "" {
		if err := json.Unmarshal([]byte(record.BindingsJSON), &settings.Bindings); err != nil {
			return WebsiteSettings{}, fmt.Errorf("decode website directory bindings: %w", err)
		}
	}
	if record.RedirectsJSON != "" {
		if err := json.Unmarshal([]byte(record.RedirectsJSON), &settings.Redirects); err != nil {
			return WebsiteSettings{}, fmt.Errorf("decode website redirects: %w", err)
		}
	}
	if record.ProxyRulesJSON != "" {
		if err := json.Unmarshal([]byte(record.ProxyRulesJSON), &settings.ProxyRules); err != nil {
			return WebsiteSettings{}, fmt.Errorf("decode website proxy rules: %w", err)
		}
	}
	return settings, nil
}

func (settings WebsiteSettings) toModel(id int64) (*models.WebsiteSetting, error) {
	bindings, err := json.Marshal(settings.Bindings)
	if err != nil {
		return nil, err
	}
	redirects, err := json.Marshal(settings.Redirects)
	if err != nil {
		return nil, err
	}
	proxies, err := json.Marshal(settings.ProxyRules)
	if err != nil {
		return nil, err
	}
	return &models.WebsiteSetting{
		WebsiteID: id, RunningDirectory: settings.RunningDirectory,
		DirectoryListing: settings.DirectoryListing, DefaultDocuments: settings.DefaultDocuments,
		AllowedIPs: settings.AllowedIPs, DeniedIPs: settings.DeniedIPs,
		RateLimitKB: settings.RateLimitKB, RateLimitAfterKB: settings.RateLimitAfterKB,
		RewriteRules: settings.RewriteRules, BindingsJSON: string(bindings),
		RedirectsJSON: string(redirects), ProxyRulesJSON: string(proxies),
		HotlinkEnabled: settings.HotlinkEnabled, HotlinkAllowEmpty: settings.HotlinkAllowEmpty,
		HotlinkDomains: settings.HotlinkDomains, HotlinkExtensions: settings.HotlinkExtensions,
		SecurityHeaders: settings.SecurityHeaders, DeniedPaths: settings.DeniedPaths,
		PHPBackend: settings.PHPBackend, TamperProtection: settings.TamperProtection,
		TrafficAlert: settings.TrafficAlert, TrafficAlertBytes: settings.TrafficAlertBytes,
		AccessLogEnabled: settings.AccessLogEnabled, ErrorLogEnabled: settings.ErrorLogEnabled,
		UpdatedAt: settings.UpdatedAt,
	}, nil
}

func renderWebsiteSettings(
	site *models.Website,
	rootDir string,
	record *models.WebsiteSetting,
) (renderedWebsiteSettings, error) {
	settings := defaultWebsiteSettings()
	if record != nil {
		var err error
		settings, err = websiteSettingsFromModel(record)
		if err != nil {
			return renderedWebsiteSettings{}, err
		}
	}
	rendered := renderedWebsiteSettings{
		DefaultDocuments: settings.DefaultDocuments,
		AutoIndex:        "off", PHPBackend: settings.PHPBackend,
		AccessLogEnabled: settings.AccessLogEnabled,
		ErrorLogEnabled:  settings.ErrorLogEnabled,
	}
	if record == nil {
		rendered.AccessLogEnabled = true
		rendered.ErrorLogEnabled = true
	}
	if settings.DirectoryListing {
		rendered.AutoIndex = "on"
	}
	if err := validateDefaultDocuments(settings.DefaultDocuments); err != nil {
		return renderedWebsiteSettings{}, err
	}
	if err := validatePHPBackend(settings.PHPBackend); err != nil {
		return renderedWebsiteSettings{}, err
	}
	if site != nil && strings.EqualFold(site.Type, "proxy") {
		rendered.PHPBackend = "unix:/dev/shm/php-cgi.sock"
	}
	if rootDir != "" && strings.TrimSpace(settings.RunningDirectory) != "" {
		runningRoot, err := managedRunningDirectory(rootDir, settings.RunningDirectory)
		if err != nil {
			return renderedWebsiteSettings{}, err
		}
		rendered.RootDir = runningRoot
	}

	rewrite, err := renderRewriteRules(settings.RewriteRules)
	if err != nil {
		return renderedWebsiteSettings{}, err
	}
	rendered.RewriteDirectives = rewrite
	serverDirectives, err := renderServerDirectives(settings)
	if err != nil {
		return renderedWebsiteSettings{}, err
	}
	rendered.ServerDirectives = serverDirectives
	extraLocations, err := renderExtraLocations(rootDir, settings)
	if err != nil {
		return renderedWebsiteSettings{}, err
	}
	rendered.ExtraLocations = extraLocations
	return rendered, nil
}

func managedRunningDirectory(root, value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(filepath.Clean("/"+value), string(filepath.Separator))
	if value == "" || value == "." {
		return root, nil
	}
	target := filepath.Join(root, value)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("运行目录必须位于网站根目录内")
	}
	return target, nil
}

func validateDefaultDocuments(value string) error {
	items := strings.Fields(value)
	if len(items) == 0 || len(items) > 20 {
		return errors.New("默认文档必须包含 1–20 个文件名")
	}
	pattern := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	for _, item := range items {
		if !pattern.MatchString(item) {
			return fmt.Errorf("默认文档 %q 格式无效", item)
		}
	}
	return nil
}

func validatePHPBackend(value string) error {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "unix:") {
		path := strings.TrimPrefix(value, "unix:")
		if !filepath.IsAbs(path) || strings.ContainsAny(path, "\r\n\t ;{}\"'$`") {
			return errors.New("PHP Unix Socket 路径无效")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return errors.New("PHP 后端必须是本机 Unix Socket 或回环地址端口")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return errors.New("PHP 后端端口无效")
	}
	return nil
}

func renderRewriteRules(value string) (string, error) {
	lines := splitSettingLines(value)
	if len(lines) > 100 {
		return "", errors.New("伪静态规则不能超过 100 行")
	}
	var result strings.Builder
	for _, line := range lines {
		if strings.ContainsAny(line, "{}\x00`") || !strings.HasSuffix(line, ";") ||
			!strings.HasPrefix(line, "rewrite ") {
			return "", fmt.Errorf("不支持的伪静态规则 %q，仅允许 rewrite 指令", line)
		}
		result.WriteString("        ")
		result.WriteString(line)
		result.WriteByte('\n')
	}
	return strings.TrimSuffix(result.String(), "\n"), nil
}

func renderServerDirectives(settings WebsiteSettings) (string, error) {
	var lines []string
	allowed, err := validateIPLines(settings.AllowedIPs)
	if err != nil {
		return "", fmt.Errorf("访问白名单: %w", err)
	}
	denied, err := validateIPLines(settings.DeniedIPs)
	if err != nil {
		return "", fmt.Errorf("访问黑名单: %w", err)
	}
	for _, value := range denied {
		lines = append(lines, "    deny "+value+";")
	}
	for _, value := range allowed {
		lines = append(lines, "    allow "+value+";")
	}
	if len(allowed) > 0 {
		lines = append(lines, "    deny all;")
	}
	if settings.RateLimitKB < 0 || settings.RateLimitKB > 10<<20 ||
		settings.RateLimitAfterKB < 0 || settings.RateLimitAfterKB > 10<<20 {
		return "", errors.New("流量限制必须在 0–10485760 KB/s 范围内")
	}
	if settings.RateLimitAfterKB > 0 {
		lines = append(lines, fmt.Sprintf("    limit_rate_after %dk;", settings.RateLimitAfterKB))
	}
	if settings.RateLimitKB > 0 {
		lines = append(lines, fmt.Sprintf("    limit_rate %dk;", settings.RateLimitKB))
	}
	if settings.SecurityHeaders {
		lines = append(lines,
			`    add_header X-Content-Type-Options "nosniff" always;`,
			`    add_header X-Frame-Options "SAMEORIGIN" always;`,
			`    add_header Referrer-Policy "strict-origin-when-cross-origin" always;`,
		)
	}
	return strings.Join(lines, "\n"), nil
}

func renderExtraLocations(rootDir string, settings WebsiteSettings) (string, error) {
	var blocks []string
	seen := make(map[string]struct{})
	for _, binding := range settings.Bindings {
		if !binding.Enabled {
			continue
		}
		path, err := validateLocationPath(binding.Path)
		if err != nil {
			return "", fmt.Errorf("子目录绑定: %w", err)
		}
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("路径 %s 被重复配置", path)
		}
		seen[path] = struct{}{}
		directory, err := managedRunningDirectory(rootDir, binding.Directory)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, fmt.Sprintf(
			"    location ^~ %s/ {\n        alias %s/;\n        index %s;\n        try_files $uri $uri/ =404;\n    }",
			strings.TrimSuffix(path, "/"), directory, settings.DefaultDocuments,
		))
	}
	for _, redirect := range settings.Redirects {
		if !redirect.Enabled {
			continue
		}
		path, err := validateLocationPath(redirect.Source)
		if err != nil {
			return "", fmt.Errorf("重定向: %w", err)
		}
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("路径 %s 被重复配置", path)
		}
		seen[path] = struct{}{}
		if redirect.Status != 301 && redirect.Status != 302 && redirect.Status != 307 && redirect.Status != 308 {
			return "", errors.New("重定向状态码只能是 301、302、307 或 308")
		}
		target, err := validateRedirectTarget(redirect.Target)
		if err != nil {
			return "", err
		}
		blocks = append(blocks, fmt.Sprintf(
			"    location = %s {\n        return %d %s;\n    }", path, redirect.Status, target,
		))
	}
	for _, proxy := range settings.ProxyRules {
		if !proxy.Enabled {
			continue
		}
		path, err := validateLocationPath(proxy.Path)
		if err != nil {
			return "", fmt.Errorf("反向代理: %w", err)
		}
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("路径 %s 被重复配置", path)
		}
		seen[path] = struct{}{}
		target, err := validateProxyTarget(proxy.Target)
		if err != nil {
			return "", err
		}
		host := strings.TrimSpace(proxy.Host)
		if host == "" {
			host = "$host"
		}
		if host != "$host" && host != "$http_host" {
			if _, err := validateDomainList(host); err != nil {
				return "", errors.New("反向代理 Host 无效")
			}
		}
		blocks = append(blocks, fmt.Sprintf(
			"    location %s {\n        proxy_pass %s;\n        proxy_set_header Host %s;\n        proxy_set_header X-Real-IP $remote_addr;\n        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n        proxy_set_header X-Forwarded-Proto $scheme;\n    }",
			path, target, host,
		))
	}
	deniedPaths := splitSettingLines(settings.DeniedPaths)
	for _, path := range deniedPaths {
		path, err := validateLocationPath(path)
		if err != nil {
			return "", fmt.Errorf("禁止访问路径: %w", err)
		}
		if _, exists := seen[path]; exists {
			return "", fmt.Errorf("路径 %s 被重复配置", path)
		}
		seen[path] = struct{}{}
		blocks = append(blocks, fmt.Sprintf("    location ^~ %s { return 403; }", path))
	}
	if settings.HotlinkEnabled {
		domains, err := validateDomainList(settings.HotlinkDomains)
		if err != nil {
			return "", fmt.Errorf("防盗链域名: %w", err)
		}
		extensions := strings.Fields(strings.ReplaceAll(settings.HotlinkExtensions, ",", " "))
		if len(extensions) == 0 || len(extensions) > 50 {
			return "", errors.New("防盗链扩展名必须包含 1–50 项")
		}
		extensionPattern := regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`)
		for _, extension := range extensions {
			if !extensionPattern.MatchString(extension) {
				return "", fmt.Errorf("防盗链扩展名 %q 无效", extension)
			}
		}
		referers := []string{"blocked", "server_names"}
		if settings.HotlinkAllowEmpty {
			referers = append([]string{"none"}, referers...)
		}
		referers = append(referers, domains...)
		blocks = append(blocks, fmt.Sprintf(
			"    location ~* \\.(%s)$ {\n        valid_referers %s;\n        if ($invalid_referer) { return 403; }\n    }",
			strings.Join(extensions, "|"), strings.Join(referers, " "),
		))
	}
	sort.Strings(blocks)
	return strings.Join(blocks, "\n"), nil
}

func validateIPLines(value string) ([]string, error) {
	values := splitSettingLines(value)
	if len(values) > 200 {
		return nil, errors.New("IP 规则不能超过 200 条")
	}
	for _, item := range values {
		if net.ParseIP(item) == nil {
			if _, _, err := net.ParseCIDR(item); err != nil {
				return nil, fmt.Errorf("IP/CIDR %q 无效", item)
			}
		}
	}
	return values, nil
}

func validateDomainList(value string) ([]string, error) {
	values := strings.Fields(strings.ReplaceAll(value, ",", " "))
	if len(values) > 100 {
		return nil, errors.New("域名不能超过 100 个")
	}
	pattern := regexp.MustCompile(`^(?:\*\.)?[A-Za-z0-9][A-Za-z0-9.-]{0,252}$`)
	for _, item := range values {
		if !pattern.MatchString(item) || strings.Contains(item, "..") {
			return nil, fmt.Errorf("域名 %q 无效", item)
		}
	}
	return values, nil
}

func validateLocationPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.HasPrefix(value, "/") || value == "/" ||
		strings.Contains(value, "..") || strings.ContainsAny(value, "\r\n\t ;{}\"'$`?") {
		return "", fmt.Errorf("站点路径 %q 无效，且不能覆盖根路径", value)
	}
	if value == "/.well-known" || strings.HasPrefix(value, "/.well-known/acme-challenge") {
		return "", errors.New("不能覆盖 ACME 证书验证路径")
	}
	return strings.TrimSuffix(value, "/"), nil
}

func validateRedirectTarget(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") && !strings.ContainsAny(value, "\r\n\t ;{}\"'`") {
		return value, nil
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("重定向目标必须是站内路径或 HTTP/HTTPS 地址")
	}
	if strings.ContainsAny(value, "\r\n\t ;{}\"'`") {
		return "", errors.New("重定向目标包含不安全字符")
	}
	return value, nil
}

func validateProxyTarget(value string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return "", errors.New("反向代理目标必须是 HTTP/HTTPS 地址")
	}
	if strings.ContainsAny(value, "\r\n\t ;{}\"'`$") {
		return "", errors.New("反向代理目标包含不安全字符")
	}
	return value, nil
}

func splitSettingLines(value string) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, item := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ','
	}) {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, exists := seen[item]; exists {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}
