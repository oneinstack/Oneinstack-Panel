package website

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
)

const (
	maxWebServerConfigBytes = 1024 * 1024
	maxWebServerConfigFiles = 500
)

var (
	webServerVersionPattern = regexp.MustCompile(`(?i)(?:nginx|openresty)/([0-9][0-9A-Za-z.+_-]*)`)
	webServerConfigMu       sync.Mutex
)

type WebServerInfo struct {
	Available              bool   `json:"available"`
	Component              string `json:"component,omitempty"`
	Name                   string `json:"name,omitempty"`
	Version                string `json:"version,omitempty"`
	Running                bool   `json:"running"`
	BinaryPath             string `json:"binaryPath,omitempty"`
	Prefix                 string `json:"prefix,omitempty"`
	ConfigRoot             string `json:"configRoot,omitempty"`
	MainConfigPath         string `json:"mainConfigPath,omitempty"`
	SiteConfigDir          string `json:"siteConfigDir,omitempty"`
	ConfigurationAvailable bool   `json:"configurationAvailable"`
}

type WebServerConfigFile struct {
	Path       string    `json:"path"`
	Name       string    `json:"name"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modifiedAt"`
	Revision   string    `json:"revision"`
	Main       bool      `json:"main"`
	Site       bool      `json:"site"`
}

type WebServerConfigDocument struct {
	WebServerConfigFile
	Content string `json:"content"`
}

type WebServerConfigUpdate struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Revision string `json:"revision"`
}

type WebServerConfigUpdateResult struct {
	WebServerConfigDocument
	BackupPath string `json:"backupPath"`
	Reloaded   bool   `json:"reloaded"`
}

type webServerCandidate struct {
	Component string
	Name      string
	Binary    string
	Prefix    string
	Config    string
	Priority  int
}

type WebServerConfigManager struct {
	Server     WebServerInfo
	Runner     CommandRunner
	BackupRoot string
}

func DetectWebServer() (WebServerInfo, error) {
	candidates := webServerCandidates()
	runningExecutables := runningExecutableSet()
	installedComponents := installedWebServerComponents()

	type rankedCandidate struct {
		candidate webServerCandidate
		score     int
	}
	var ranked []rankedCandidate
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		binary := filepath.Clean(strings.TrimSpace(candidate.Binary))
		if binary == "." || !filepath.IsAbs(binary) {
			continue
		}
		canonical := canonicalPath(binary)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		info, err := os.Stat(binary)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			continue
		}
		score := candidate.Priority
		if runningExecutables[canonical] {
			score += 1000
		}
		if installedComponents[candidate.Component] {
			score += 100
		}
		ranked = append(ranked, rankedCandidate{candidate: candidate, score: score})
	}
	if len(ranked) == 0 {
		return WebServerInfo{}, fmt.Errorf(
			"%w: no supported Nginx or OpenResty executable was found",
			ErrWebServerUnavailable,
		)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		return ranked[i].score > ranked[j].score
	})

	selected := ranked[0].candidate
	running := make([]webServerCandidate, 0, len(ranked))
	for _, item := range ranked {
		if runningExecutables[canonicalPath(filepath.Clean(item.candidate.Binary))] {
			running = append(running, item.candidate)
		}
	}
	if len(running) > 1 {
		return WebServerInfo{}, fmt.Errorf(
			"%w: multiple running web servers were detected; select a single managed instance",
			ErrWebServerUnavailable,
		)
	}
	if len(running) == 1 {
		selected = running[0]
	}

	configRoot := filepath.Clean(selected.Config)
	prefix := filepath.Clean(selected.Prefix)
	if detectedPrefix, detectedConfig, ok := inspectWebServerLayout(selected.Binary); ok {
		prefix = detectedPrefix
		configRoot = detectedConfig
	}
	mainConfig := filepath.Join(configRoot, "nginx.conf")
	configurationAvailable := isRegularFile(mainConfig)
	siteConfigDir := detectSiteConfigDir(configRoot, mainConfig)
	version := inspectWebServerVersion(selected.Binary)

	return WebServerInfo{
		Available:              true,
		Component:              selected.Component,
		Name:                   selected.Name,
		Version:                version,
		Running:                runningExecutables[canonicalPath(selected.Binary)],
		BinaryPath:             filepath.Clean(selected.Binary),
		Prefix:                 prefix,
		ConfigRoot:             configRoot,
		MainConfigPath:         mainConfig,
		SiteConfigDir:          siteConfigDir,
		ConfigurationAvailable: configurationAvailable,
	}, nil
}

func WebServerStatus() WebServerInfo {
	server, err := DetectWebServer()
	if err != nil {
		return WebServerInfo{Available: false}
	}
	return server
}

func NewDefaultWebServerConfigManager() (*WebServerConfigManager, error) {
	server, err := DetectWebServer()
	if err != nil {
		return nil, err
	}
	if !server.ConfigurationAvailable {
		return nil, fmt.Errorf(
			"%w: %s main configuration is unavailable",
			ErrWebServerUnavailable,
			server.Name,
		)
	}
	return newWebServerConfigManager(server), nil
}

func newWebServerConfigManager(server WebServerInfo) *WebServerConfigManager {
	return &WebServerConfigManager{
		Server:     server,
		Runner:     OSCommandRunner{},
		BackupRoot: filepath.Join(app.GetBasePath(), "backups", "web-server-config"),
	}
}

func (manager *WebServerConfigManager) List() ([]WebServerConfigFile, error) {
	if err := manager.validate(); err != nil {
		return nil, err
	}
	files := make([]WebServerConfigFile, 0)
	err := filepath.WalkDir(manager.Server.ConfigRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == manager.Server.ConfigRoot {
			return nil
		}
		relative, err := filepath.Rel(manager.Server.ConfigRoot, path)
		if err != nil {
			return err
		}
		depth := strings.Count(filepath.Clean(relative), string(filepath.Separator))
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 || depth >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 ||
			!strings.EqualFold(filepath.Ext(entry.Name()), ".conf") {
			return nil
		}
		if len(files) >= maxWebServerConfigFiles {
			return fmt.Errorf("web server configuration contains more than %d files", maxWebServerConfigFiles)
		}
		document, err := manager.readPath(relative, false)
		if err != nil {
			return err
		}
		files = append(files, document.WebServerConfigFile)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list web server configuration: %w", err)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Main != files[j].Main {
			return files[i].Main
		}
		if files[i].Site != files[j].Site {
			return files[i].Site
		}
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func (manager *WebServerConfigManager) Read(relativePath string) (WebServerConfigDocument, error) {
	if err := manager.validate(); err != nil {
		return WebServerConfigDocument{}, err
	}
	return manager.readPath(relativePath, true)
}

// ValidateContent checks a proposed managed server configuration without
// touching the live configuration tree or reloading the service.
func (manager *WebServerConfigManager) ValidateContent(ctx context.Context, content string) error {
	if err := manager.validate(); err != nil {
		return err
	}
	if len(content) > maxWebServerConfigBytes {
		return fmt.Errorf("configuration exceeds the %d byte limit", maxWebServerConfigBytes)
	}
	if strings.ContainsRune(content, 0) {
		return errors.New("configuration contains a NUL byte")
	}

	directory, err := os.MkdirTemp(app.GetBasePath(), ".oneinstack-web-server-preview-")
	if err != nil {
		return fmt.Errorf("create temporary configuration directory: %w", err)
	}
	defer os.RemoveAll(directory)
	configDir := filepath.Join(directory, "conf.d")
	if err := os.MkdirAll(configDir, 0750); err != nil {
		return fmt.Errorf("create temporary configuration directory: %w", err)
	}
	candidate := filepath.Join(configDir, "candidate.conf")
	if err := os.WriteFile(candidate, []byte(content), 0640); err != nil {
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	mainConfig := filepath.Join(directory, "nginx.conf")
	mainContent := "events {}\nhttp {\n"
	if mimeTypes := filepath.Join(manager.Server.ConfigRoot, "mime.types"); isRegularFile(mimeTypes) {
		mainContent += fmt.Sprintf("include %s;\n", mimeTypes)
	}
	mainContent += "include conf.d/candidate.conf;\n}\n"
	if err := os.WriteFile(mainConfig, []byte(mainContent), 0640); err != nil {
		return fmt.Errorf("write temporary main configuration: %w", err)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return manager.run(timeoutCtx, "-t", "-p", ensureTrailingSeparator(directory), "-c", mainConfig)
}

func (manager *WebServerConfigManager) Update(
	ctx context.Context,
	request WebServerConfigUpdate,
) (WebServerConfigUpdateResult, error) {
	webServerConfigMu.Lock()
	defer webServerConfigMu.Unlock()

	if err := manager.validate(); err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	if len(request.Content) > maxWebServerConfigBytes {
		return WebServerConfigUpdateResult{}, fmt.Errorf(
			"configuration exceeds the %d byte limit",
			maxWebServerConfigBytes,
		)
	}
	if strings.ContainsRune(request.Content, 0) {
		return WebServerConfigUpdateResult{}, errors.New("configuration contains a NUL byte")
	}
	if len(request.Revision) != sha256.Size*2 {
		return WebServerConfigUpdateResult{}, errors.New("configuration revision is invalid")
	}

	current, target, err := manager.readResolvedPath(request.Path, true)
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	if !strings.EqualFold(current.Revision, strings.TrimSpace(request.Revision)) {
		return WebServerConfigUpdateResult{}, fmt.Errorf(
			"%w: configuration changed after it was opened; reload it before saving",
			ErrWebServerConfigConflict,
		)
	}
	if current.Content == request.Content {
		return WebServerConfigUpdateResult{
			WebServerConfigDocument: current,
			Reloaded:                false,
		}, nil
	}

	backupPath, err := manager.backup(current.Path, target)
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	original := []byte(current.Content)
	fileInfo, err := os.Stat(target)
	if err != nil {
		return WebServerConfigUpdateResult{}, fmt.Errorf("stat configuration before update: %w", err)
	}
	if err := atomicWriteConfig(filepath.Dir(target), target, []byte(request.Content)); err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	if err := os.Chmod(target, fileInfo.Mode().Perm()); err != nil {
		_ = atomicWriteConfig(filepath.Dir(target), target, original)
		return WebServerConfigUpdateResult{}, fmt.Errorf("restore configuration permissions: %w", err)
	}

	if err := manager.testConfiguration(ctx); err != nil {
		validationErr := fmt.Errorf("%w: %v", ErrWebServerConfigValidate, err)
		restoreErr := restoreWebServerConfig(target, original, fileInfo.Mode())
		if restoreErr != nil {
			return WebServerConfigUpdateResult{}, fmt.Errorf(
				"%w; restore failed: %v",
				validationErr,
				restoreErr,
			)
		}
		return WebServerConfigUpdateResult{}, fmt.Errorf(
			"%w; previous content restored",
			validationErr,
		)
	}

	reloaded := false
	if manager.Server.Running {
		if err := manager.reload(ctx); err != nil {
			restoreErr := restoreWebServerConfig(target, original, fileInfo.Mode())
			if restoreErr == nil {
				_ = manager.testConfiguration(context.Background())
				_ = manager.reload(context.Background())
			}
			if restoreErr != nil {
				return WebServerConfigUpdateResult{}, fmt.Errorf(
					"reload failed: %w; restore failed: %v",
					err,
					restoreErr,
				)
			}
			return WebServerConfigUpdateResult{}, fmt.Errorf(
				"reload failed; previous content restored: %w",
				err,
			)
		}
		reloaded = true
	}

	updated, err := manager.readPath(request.Path, true)
	if err != nil {
		return WebServerConfigUpdateResult{}, err
	}
	return WebServerConfigUpdateResult{
		WebServerConfigDocument: updated,
		BackupPath:              backupPath,
		Reloaded:                reloaded,
	}, nil
}

func (manager *WebServerConfigManager) validate() error {
	if manager == nil {
		return errors.New("web server configuration manager is not initialized")
	}
	if !manager.Server.Available ||
		!filepath.IsAbs(manager.Server.ConfigRoot) ||
		!filepath.IsAbs(manager.Server.MainConfigPath) ||
		!filepath.IsAbs(manager.Server.BinaryPath) {
		return ErrWebServerUnavailable
	}
	if manager.Runner == nil {
		return errors.New("web server command runner is not configured")
	}
	if !filepath.IsAbs(manager.BackupRoot) ||
		filepath.Clean(manager.BackupRoot) == string(filepath.Separator) {
		return errors.New("web server configuration backup root is invalid")
	}
	return nil
}

func (manager *WebServerConfigManager) readPath(
	relativePath string,
	includeContent bool,
) (WebServerConfigDocument, error) {
	document, _, err := manager.readResolvedPath(relativePath, includeContent)
	return document, err
}

func (manager *WebServerConfigManager) readResolvedPath(
	relativePath string,
	includeContent bool,
) (WebServerConfigDocument, string, error) {
	target, normalized, err := resolveManagedConfigPath(manager.Server.ConfigRoot, relativePath)
	if err != nil {
		return WebServerConfigDocument{}, "", err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return WebServerConfigDocument{}, "", fmt.Errorf("stat web server configuration: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return WebServerConfigDocument{}, "", errors.New("configuration is not a regular file")
	}
	if info.Size() > maxWebServerConfigBytes {
		return WebServerConfigDocument{}, "", fmt.Errorf(
			"configuration exceeds the %d byte limit",
			maxWebServerConfigBytes,
		)
	}
	data, err := readBoundedFile(target, maxWebServerConfigBytes)
	if err != nil {
		return WebServerConfigDocument{}, "", err
	}
	revision := sha256.Sum256(data)
	siteRelative, _ := filepath.Rel(manager.Server.ConfigRoot, manager.Server.SiteConfigDir)
	document := WebServerConfigDocument{
		WebServerConfigFile: WebServerConfigFile{
			Path:       filepath.ToSlash(normalized),
			Name:       filepath.Base(normalized),
			Size:       int64(len(data)),
			ModifiedAt: info.ModTime().UTC(),
			Revision:   hex.EncodeToString(revision[:]),
			Main:       samePath(target, manager.Server.MainConfigPath),
			Site:       pathWithin(normalized, siteRelative),
		},
	}
	if includeContent {
		document.Content = string(data)
	}
	return document, target, nil
}

func (manager *WebServerConfigManager) backup(relativePath, source string) (string, error) {
	directory := filepath.Join(
		manager.BackupRoot,
		time.Now().UTC().Format("20060102T150405.000000000Z"),
	)
	target := filepath.Join(directory, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
		return "", fmt.Errorf("create configuration backup directory: %w", err)
	}
	if err := copyRegularFile(source, target); err != nil {
		return "", fmt.Errorf("backup configuration: %w", err)
	}
	return target, nil
}

func (manager *WebServerConfigManager) testConfiguration(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return manager.run(
		timeoutCtx,
		"-t",
		"-p",
		ensureTrailingSeparator(manager.Server.Prefix),
		"-c",
		manager.Server.MainConfigPath,
	)
}

func (manager *WebServerConfigManager) reload(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return manager.run(timeoutCtx, "-s", "reload")
}

func (manager *WebServerConfigManager) run(ctx context.Context, args ...string) error {
	output, err := manager.Runner.Run(ctx, manager.Server.BinaryPath, args...)
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > 2000 {
		message = message[:2000]
	}
	if message == "" {
		message = err.Error()
	}
	return errors.New(message)
}

func webServerCandidates() []webServerCandidate {
	var candidates []webServerCandidate
	if binary := strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_BIN")); binary != "" {
		component := strings.ToLower(strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER")))
		if component != "openresty" {
			component = "nginx"
		}
		prefix := strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_PREFIX"))
		configRoot := strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_CONFIG_ROOT"))
		if prefix == "" {
			prefix = filepath.Dir(filepath.Dir(binary))
		}
		if configRoot == "" {
			configRoot = filepath.Join(prefix, "conf")
		}
		name := "Nginx"
		if component == "openresty" {
			name = "OpenResty"
		}
		candidates = append(candidates, webServerCandidate{
			Component: component,
			Name:      name,
			Binary:    binary,
			Prefix:    prefix,
			Config:    configRoot,
			Priority:  100,
		})
	}
	if binary := strings.TrimSpace(os.Getenv("ONEINSTACK_NGINX_BIN")); binary != "" {
		prefix := filepath.Dir(filepath.Dir(binary))
		candidates = append(candidates, webServerCandidate{
			Component: "nginx",
			Name:      "Nginx",
			Binary:    binary,
			Prefix:    prefix,
			Config:    filepath.Join(prefix, "conf"),
			Priority:  90,
		})
	}
	candidates = append(candidates,
		webServerCandidate{
			Component: "nginx",
			Name:      "Nginx",
			Binary:    defaultNginxBinary,
			Prefix:    "/usr/local/nginx",
			Config:    "/usr/local/nginx/conf",
			Priority:  40,
		},
		webServerCandidate{
			Component: "openresty",
			Name:      "OpenResty",
			Binary:    "/usr/local/openresty/nginx/sbin/nginx",
			Prefix:    "/usr/local/openresty/nginx",
			Config:    "/usr/local/openresty/nginx/conf",
			Priority:  40,
		},
		webServerCandidate{
			Component: "nginx",
			Name:      "Nginx",
			Binary:    "/usr/sbin/nginx",
			// Ubuntu/Debian packages keep the runtime prefix under
			// /usr/share/nginx while the main configuration lives in
			// /etc/nginx. Nginx resolves relative module and temp paths
			// from this prefix when the manager runs -t or reload.
			Prefix:   "/usr/share/nginx",
			Config:   "/etc/nginx",
			Priority: 20,
		},
		webServerCandidate{
			Component: "nginx",
			Name:      "Nginx",
			Binary:    "/usr/local/sbin/nginx",
			Prefix:    "/usr/local",
			Config:    "/usr/local/etc/nginx",
			Priority:  10,
		},
	)
	for _, executable := range []string{"openresty", "nginx"} {
		if path, err := exec.LookPath(executable); err == nil {
			component := "nginx"
			name := "Nginx"
			prefix := "/etc/nginx"
			config := "/etc/nginx"
			if executable == "openresty" || strings.Contains(strings.ToLower(path), "openresty") {
				component = "openresty"
				name = "OpenResty"
				prefix = "/usr/local/openresty/nginx"
				config = filepath.Join(prefix, "conf")
			} else if path == "/usr/sbin/nginx" {
				// Ubuntu/Debian package layout: binary in /usr/sbin,
				// runtime prefix in /usr/share/nginx, config in /etc/nginx.
				prefix = "/usr/share/nginx"
				config = "/etc/nginx"
			}
			candidates = append(candidates, webServerCandidate{
				Component: component,
				Name:      name,
				Binary:    path,
				Prefix:    prefix,
				Config:    config,
			})
		}
	}
	return candidates
}

func installedWebServerComponents() map[string]bool {
	result := make(map[string]bool)
	if app.DB() == nil {
		return result
	}
	var rows []models.Software
	if err := app.DB().
		Select("component").
		Where("installed = ? AND component IN ?", true, []string{"nginx", "openresty"}).
		Find(&rows).Error; err != nil {
		return result
	}
	for _, row := range rows {
		result[strings.ToLower(strings.TrimSpace(row.Component))] = true
	}
	return result
}

func runningExecutableSet() map[string]bool {
	result := make(map[string]bool)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return result
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		executable, err := os.Readlink(filepath.Join("/proc", entry.Name(), "exe"))
		if err != nil {
			continue
		}
		result[canonicalPath(strings.TrimSuffix(executable, " (deleted)"))] = true
	}
	return result
}

func inspectWebServerVersion(binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "-v").CombinedOutput()
	if err != nil && len(output) == 0 {
		return ""
	}
	match := webServerVersionPattern.FindStringSubmatch(string(output))
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func inspectWebServerLayout(binary string) (string, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, _ := exec.CommandContext(ctx, binary, "-V").CombinedOutput()
	text := string(output)
	prefix := parseConfigureArgument(text, "--prefix=")
	configPath := parseConfigureArgument(text, "--conf-path=")
	if prefix == "" || configPath == "" || !filepath.IsAbs(prefix) || !filepath.IsAbs(configPath) {
		return "", "", false
	}
	return filepath.Clean(prefix), filepath.Clean(filepath.Dir(configPath)), true
}

func parseConfigureArgument(output, key string) string {
	for _, field := range strings.Fields(output) {
		if strings.HasPrefix(field, key) {
			return strings.Trim(strings.TrimPrefix(field, key), "'")
		}
	}
	return ""
}

func detectSiteConfigDir(configRoot, mainConfig string) string {
	content, _ := readBoundedFile(mainConfig, maxWebServerConfigBytes)
	normalized := filepath.ToSlash(string(content))
	for _, name := range []string{"vhost", "conf.d"} {
		candidate := filepath.Join(configRoot, name)
		if strings.Contains(normalized, "/"+name+"/") ||
			strings.Contains(normalized, name+"/*.conf") {
			return candidate
		}
	}
	for _, name := range []string{"vhost", "conf.d"} {
		candidate := filepath.Join(configRoot, name)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return filepath.Join(configRoot, "conf.d")
}

func resolveManagedConfigPath(root, relativePath string) (string, string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return "", "", errors.New("configuration root is invalid")
	}
	relativePath = filepath.FromSlash(strings.TrimSpace(relativePath))
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || filepath.IsAbs(cleaned) ||
		cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) ||
		!strings.EqualFold(filepath.Ext(cleaned), ".conf") {
		return "", "", errors.New("configuration path is invalid")
	}
	target := filepath.Join(root, cleaned)
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", errors.New("configuration path escapes the managed root")
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", "", fmt.Errorf("inspect configuration path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("configuration path contains a symbolic link")
		}
	}
	return target, relative, nil
}

func readBoundedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open configuration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("configuration exceeds the %d byte limit", limit)
	}
	return data, nil
}

func copyRegularFile(source, target string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("source configuration is not a regular file")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, sourceInfo.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, io.LimitReader(input, maxWebServerConfigBytes+1)); err != nil {
		output.Close()
		return err
	}
	if err := output.Sync(); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}

func restoreWebServerConfig(target string, content []byte, mode os.FileMode) error {
	if err := atomicWriteConfig(filepath.Dir(target), target, content); err != nil {
		return err
	}
	return os.Chmod(target, mode.Perm())
}

func canonicalPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func samePath(left, right string) bool {
	return canonicalPath(left) == canonicalPath(right)
}

func pathWithin(relativePath, directory string) bool {
	directory = filepath.Clean(directory)
	if directory == "." || directory == "" ||
		directory == ".." || strings.HasPrefix(directory, ".."+string(filepath.Separator)) {
		return false
	}
	relativePath = filepath.Clean(relativePath)
	return relativePath == directory ||
		strings.HasPrefix(relativePath, directory+string(filepath.Separator))
}

func ensureTrailingSeparator(path string) string {
	cleaned := filepath.Clean(path)
	if strings.HasSuffix(cleaned, string(filepath.Separator)) {
		return cleaned
	}
	return cleaned + string(filepath.Separator)
}
