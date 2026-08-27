package website

import (
	"bufio"
	"bytes"
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
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
)

const (
	maxWebServerConfigBytes         = 1024 * 1024
	maxWebServerConfigFiles         = 500
	maxWebServerIncludeDepth        = 32
	maxWebServerPreviewDependencies = 500
)

var (
	webServerVersionPattern = regexp.MustCompile(`(?i)(?:nginx|openresty|tengine)/([0-9][0-9A-Za-z.+_-]*)|Apache/([0-9][0-9A-Za-z.+_-]*)|v?([0-9]+\.[0-9]+(?:\.[0-9]+)*)`)
	caddyVersionPattern     = regexp.MustCompile(`(?m)^\s*v?([0-9]+\.[0-9]+\.[0-9]+)(?:[-+][0-9A-Za-z.-]+)?(?:\s|$)`)
	webServerConfigMu       sync.Mutex
	systemdExecPathPattern  = regexp.MustCompile(`(?:^|[ {;])path=([^ ;}]+)`)
	systemdAbsPathPattern   = regexp.MustCompile(`(/[^ ;}{]+)`)
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
	ServiceName            string `json:"serviceName,omitempty"`
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
	BackupPath   string `json:"backupPath"`
	Reloaded     bool   `json:"reloaded"`
	ServiceState string `json:"serviceState"`
	ApplyStatus  string `json:"applyStatus"`
}

const (
	WebServerServiceStateRunning    = "running"
	WebServerServiceStateNotRunning = "not_running"

	WebServerConfigApplyStatusSavedNotReloaded   = "saved_not_reloaded"
	WebServerConfigApplyStatusReloaded           = "reloaded"
	WebServerConfigApplyStatusReloadFailedRolled = "reload_failed_rolled_back"
)

// WebServerConfigApplyError preserves the user-visible outcome when a reload
// fails after the new configuration was written. Callers can distinguish a
// rolled-back reload failure from validation or file-write failures without
// inspecting an unsafe command error string.
type WebServerConfigApplyError struct {
	Status string
	Err    error
}

func (err *WebServerConfigApplyError) Error() string {
	if err == nil || err.Err == nil {
		return "web server configuration apply failed"
	}
	return err.Err.Error()
}

func (err *WebServerConfigApplyError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

type webServerCandidate struct {
	Component string
	Name      string
	Service   string
	Binary    string
	Prefix    string
	Config    string
	Priority  int
}

type systemdShowLookup func(unit string, properties ...string) (map[string]string, error)

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
			"%w: no supported Web server executable was found",
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
	runtimeCandidate := selected
	runtimeCandidate.Prefix = prefix
	runtimeCandidate.Config = configRoot
	mainConfigName := "nginx.conf"
	if selected.Component == "apache" {
		mainConfigName = "httpd.conf"
	} else if selected.Component == "caddy" {
		mainConfigName = "Caddyfile"
	}
	mainConfig := filepath.Join(configRoot, mainConfigName)
	configurationAvailable := isRegularFile(mainConfig)
	siteConfigDir := detectSiteConfigDir(configRoot, mainConfig)
	version := inspectWebServerVersion(selected.Component, selected.Binary)

	return WebServerInfo{
		Available:              true,
		Component:              selected.Component,
		Name:                   selected.Name,
		Version:                version,
		Running:                webServerIsRunning(runtimeCandidate, runningExecutables),
		BinaryPath:             filepath.Clean(selected.Binary),
		Prefix:                 prefix,
		ConfigRoot:             configRoot,
		MainConfigPath:         mainConfig,
		SiteConfigDir:          siteConfigDir,
		ServiceName:            selected.Service,
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
			(!strings.EqualFold(filepath.Ext(entry.Name()), ".conf") &&
				!samePath(path, manager.Server.MainConfigPath)) {
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
func (manager *WebServerConfigManager) ValidateContent(ctx context.Context, relativePath, content string) error {
	return manager.validateContent(ctx, manager.Server.ConfigRoot, relativePath, content)
}

// ValidateContentAtRoot validates a candidate file below an explicitly
// managed configuration root while retaining the detected Web server runtime
// context (prefix, real config root, main config and binary). Website vhosts
// may live in a separate managed root, so using Server.ConfigRoot for both
// purposes would reject valid candidates or lose relative include
// dependencies.
func (manager *WebServerConfigManager) ValidateContentAtRoot(
	ctx context.Context,
	configRoot, relativePath, content string,
) error {
	return manager.validateContent(ctx, configRoot, relativePath, content)
}

// configIncludeRoots returns the runtime configuration roots that a managed
// configuration is allowed to reference during preview. The website vhost
// tree is intentionally separate from the detected OpenResty/Nginx config
// root, but it is still a Panel-managed dependency.
func (manager *WebServerConfigManager) configIncludeRoots() []string {
	if manager == nil {
		return nil
	}
	roots := []string{manager.Server.ConfigRoot, manager.Server.Prefix}
	if engine, err := normalizeWebsiteEngine(manager.Server.Component); err == nil {
		roots = append(roots, managedVhostDir(engine))
	}
	return uniqueCleanPaths(roots)
}

func (manager *WebServerConfigManager) validateContent(
	ctx context.Context,
	configRoot, relativePath, content string,
) error {
	if err := manager.validate(); err != nil {
		return err
	}
	configRoot = filepath.Clean(strings.TrimSpace(configRoot))
	if !filepath.IsAbs(configRoot) || configRoot == string(filepath.Separator) {
		return errors.New("configuration root is invalid")
	}
	if len(content) > maxWebServerConfigBytes {
		return fmt.Errorf("configuration exceeds the %d byte limit", maxWebServerConfigBytes)
	}
	if strings.ContainsRune(content, 0) {
		return errors.New("configuration contains a NUL byte")
	}
	// A create preview validates a candidate file before it exists. Resolve the
	// path lexically and inspect existing components for symlinks, while leaving
	// creation of the missing directory/file to the execution publisher.
	target, _, err := resolveManagedConfigPathForPreview(configRoot, relativePath)
	if err != nil {
		return err
	}
	if manager.Server.Component == "apache" || manager.Server.Component == "caddy" {
		preview, err := os.CreateTemp(app.GetBasePath(), ".oneinstack-web-server-preview-*")
		if err != nil {
			return fmt.Errorf("create temporary %s configuration: %w", manager.Server.Component, err)
		}
		previewPath := preview.Name()
		defer os.Remove(previewPath)
		if err := preview.Chmod(0640); err != nil {
			_ = preview.Close()
			return fmt.Errorf("secure temporary %s configuration: %w", manager.Server.Component, err)
		}
		if _, err := preview.WriteString(content); err != nil {
			_ = preview.Close()
			return fmt.Errorf("write temporary %s configuration: %w", manager.Server.Component, err)
		}
		if err := preview.Close(); err != nil {
			return fmt.Errorf("close temporary %s configuration: %w", manager.Server.Component, err)
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if manager.Server.Component == "apache" {
			return manager.runCommand(timeoutCtx, manager.Server.BinaryPath, "-t", "-f", previewPath)
		}
		return manager.runCommand(timeoutCtx, manager.Server.BinaryPath, "validate", "--config", previewPath, "--adapter", "caddyfile")
	}

	directory, err := os.MkdirTemp(app.GetBasePath(), ".oneinstack-web-server-preview-")
	if err != nil {
		return fmt.Errorf("create temporary configuration directory: %w", err)
	}
	defer os.RemoveAll(directory)
	mainConfig := filepath.Join(directory, "nginx.conf")
	previewMainContent := ""
	if samePath(target, manager.Server.MainConfigPath) {
		// The main file must be validated as the root configuration. Putting it
		// inside http{} makes valid top-level directives such as user/events/http
		// appear in the wrong context and always fail validation.
		if err := stageRelativeConfigIncludes(directory, manager.Server.ConfigRoot); err != nil {
			return err
		}
		previewMainContent = content
		if err := os.WriteFile(mainConfig, []byte(previewMainContent), 0640); err != nil {
			return fmt.Errorf("write temporary main configuration: %w", err)
		}
	} else {
		configDir := filepath.Join(directory, "conf.d")
		if err := os.MkdirAll(configDir, 0750); err != nil {
			return fmt.Errorf("create temporary configuration directory: %w", err)
		}
		candidate := filepath.Join(configDir, "candidate.conf")
		if err := os.WriteFile(candidate, []byte(content), 0640); err != nil {
			return fmt.Errorf("write temporary configuration: %w", err)
		}
		mainContent := "events {}\nhttp {\n"
		if mimeTypes := filepath.Join(manager.Server.ConfigRoot, "mime.types"); isRegularFile(mimeTypes) {
			mainContent += fmt.Sprintf("include %s;\n", mimeTypes)
		}
		mainContent += "include conf.d/candidate.conf;\n}\n"
		previewMainContent = mainContent
		if err := os.WriteFile(mainConfig, []byte(previewMainContent), 0640); err != nil {
			return fmt.Errorf("write temporary main configuration: %w", err)
		}
	}
	// The validation runs with a temporary prefix. Stage common relative
	// includes beside the temporary main configuration so both main and site
	// configurations resolve the same dependencies as the live installation.
	if err := stageRelativeConfigFiles(
		directory,
		manager.Server.Prefix,
		manager.Server.ConfigRoot,
	); err != nil {
		return err
	}
	includeContents := []string{content}
	if samePath(target, manager.Server.MainConfigPath) {
		// Main configuration content is the actual proposed root file and its
		// relative includes must be staged. The site preview main file is
		// synthetic and is intentionally excluded from this scan because its
		// candidate include exists only inside the disposable directory.
		includeContents = append(includeContents, previewMainContent)
	}
	if err := stageCustomConfigIncludes(
		directory,
		manager.Server.ConfigRoot,
		manager.configIncludeRoots(),
		includeContents...,
	); err != nil {
		return err
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return manager.run(timeoutCtx, "-t", "-p", ensureTrailingSeparator(directory), "-c", mainConfig)
}

var nginxIncludePattern = regexp.MustCompile(`(?m)^[\t ]*include[\t ]+([^;#]+);`)

// stageCustomConfigIncludes stages fixed custom include dependencies while
// preserving their relative paths in the disposable prefix. Dynamic includes
// cannot be resolved safely during preview and are rejected explicitly.
func stageCustomConfigIncludes(directory, configRoot string, allowedRoots []string, contents ...string) error {
	visited := make(map[string]struct{})
	staged := 0
	for _, content := range contents {
		if err := collectConfigIncludes(
			directory,
			filepath.Clean(configRoot),
			allowedRoots,
			filepath.Clean(configRoot),
			content,
			0,
			visited,
			&staged,
		); err != nil {
			return err
		}
	}
	return nil
}

func collectConfigIncludes(
	directory, configRoot string, allowedRoots []string, baseDir, content string,
	depth int,
	visited map[string]struct{}, staged *int,
) error {
	if depth > maxWebServerIncludeDepth {
		return fmt.Errorf("web server configuration include nesting exceeds %d levels", maxWebServerIncludeDepth)
	}
	for _, match := range nginxIncludePattern.FindAllStringSubmatch(content, -1) {
		expression := strings.TrimSpace(match[1])
		if len(expression) >= 2 && ((expression[0] == '"' && expression[len(expression)-1] == '"') ||
			(expression[0] == '\'' && expression[len(expression)-1] == '\'')) {
			expression = expression[1 : len(expression)-1]
		}
		if expression == "" {
			return errors.New("web server configuration include path is empty")
		}
		if strings.ContainsRune(expression, '$') {
			return fmt.Errorf("dynamic web server configuration include is not supported: %s", expression)
		}

		matches, root, err := resolveConfigInclude(expression, baseDir, allowedRoots)
		if err != nil {
			return err
		}
		if len(matches) == 0 {
			if hasGlobPattern(expression) {
				continue
			}
			return fmt.Errorf("web server configuration include is missing: %s", expression)
		}
		for _, source := range matches {
			info, err := os.Lstat(source)
			if err != nil {
				return fmt.Errorf("inspect web server include %s: %w", expression, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("web server configuration include cannot be a symbolic link: %s", expression)
			}
			if !info.Mode().IsRegular() {
				continue
			}
			canonical := canonicalPath(source)
			if _, exists := visited[canonical]; exists {
				continue
			}
			visited[canonical] = struct{}{}
			(*staged)++
			if *staged > maxWebServerPreviewDependencies {
				return fmt.Errorf("web server configuration includes exceed %d files", maxWebServerPreviewDependencies)
			}

			data, err := readBoundedFile(source, maxWebServerConfigBytes)
			if err != nil {
				return fmt.Errorf("read web server include %s: %w", expression, err)
			}
			if !filepath.IsAbs(expression) {
				relative, relErr := filepath.Rel(root, source)
				if relErr != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
					return fmt.Errorf("web server configuration include escapes its root: %s", expression)
				}
				target := filepath.Join(directory, relative)
				if err := os.MkdirAll(filepath.Dir(target), 0750); err != nil {
					return fmt.Errorf("create web server include directory: %w", err)
				}
				if err := os.WriteFile(target, data, 0640); err != nil {
					return fmt.Errorf("stage web server include %s: %w", expression, err)
				}
			}

			if err := collectConfigIncludes(
				directory,
				configRoot,
				allowedRoots,
				filepath.Dir(source),
				string(data),
				depth+1,
				visited,
				staged,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func resolveConfigInclude(expression, baseDir string, allowedRoots []string) ([]string, string, error) {
	if filepath.IsAbs(expression) {
		matches, err := filepath.Glob(filepath.Clean(expression))
		if err != nil {
			return nil, "", fmt.Errorf("invalid web server configuration include %s: %w", expression, err)
		}
		for _, match := range matches {
			if !pathWithinAnyRoot(match, allowedRoots) {
				return nil, "", fmt.Errorf("web server configuration include is outside the managed roots: %s", expression)
			}
		}
		return matches, "", nil
	}
	cleaned := filepath.Clean(filepath.FromSlash(expression))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return nil, "", fmt.Errorf("web server configuration include escapes the managed root: %s", expression)
	}
	roots := append([]string{filepath.Clean(baseDir)}, allowedRoots...)
	for _, root := range uniqueCleanPaths(roots) {
		candidate := filepath.Join(root, cleaned)
		matches, err := filepath.Glob(candidate)
		if err != nil {
			return nil, "", fmt.Errorf("invalid web server configuration include %s: %w", expression, err)
		}
		if len(matches) > 0 {
			return matches, root, nil
		}
	}
	return nil, "", nil
}

func hasGlobPattern(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func pathWithinRoot(path, root string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func pathWithinAnyRoot(path string, roots []string) bool {
	for _, root := range roots {
		if pathWithinRoot(path, root) {
			return true
		}
	}
	return false
}

func uniqueCleanPaths(paths []string) []string {
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		cleaned := filepath.Clean(strings.TrimSpace(path))
		if cleaned == "." || cleaned == string(filepath.Separator) {
			continue
		}
		if _, exists := seen[cleaned]; exists {
			continue
		}
		seen[cleaned] = struct{}{}
		result = append(result, cleaned)
	}
	return result
}

// stageRelativeConfigFiles stages the common files referenced by Nginx
// configurations with relative include paths. Prefer the detected config root,
// then the detected runtime prefix to support custom Nginx/OpenResty layouts.
func stageRelativeConfigFiles(directory, prefix, configRoot string) error {
	for _, name := range []string{
		"mime.types",
		"fastcgi_params",
		"scgi_params",
		"uwsgi_params",
		"koi-utf",
		"koi-win",
		"win-utf",
	} {
		source := ""
		for _, root := range []string{configRoot, prefix} {
			candidate := filepath.Join(root, name)
			if isRegularFile(candidate) {
				source = candidate
				break
			}
		}
		if source == "" {
			continue
		}
		data, readErr := readBoundedFile(source, maxWebServerConfigBytes)
		if readErr != nil {
			return fmt.Errorf("read web server include %s: %w", name, readErr)
		}
		if writeErr := os.WriteFile(filepath.Join(directory, name), data, 0640); writeErr != nil {
			return fmt.Errorf("stage web server include %s: %w", name, writeErr)
		}
	}
	return nil
}

// stageRelativeConfigIncludes makes relative includes resolve against the
// disposable prefix while keeping the live configuration tree untouched.
func stageRelativeConfigIncludes(directory, configRoot string) error {
	for _, name := range []string{"conf.d", "sites-enabled", "modules-enabled"} {
		sourceDir := filepath.Join(configRoot, name)
		entries, err := os.ReadDir(sourceDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read web server include directory %s: %w", name, err)
		}
		targetDir := filepath.Join(directory, name)
		if err := os.MkdirAll(targetDir, 0750); err != nil {
			return fmt.Errorf("create web server include directory %s: %w", name, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			data, err := readBoundedFile(filepath.Join(sourceDir, entry.Name()), maxWebServerConfigBytes)
			if err != nil {
				return fmt.Errorf("read web server include %s/%s: %w", name, entry.Name(), err)
			}
			if err := os.WriteFile(filepath.Join(targetDir, entry.Name()), data, 0640); err != nil {
				return fmt.Errorf("stage web server include %s/%s: %w", name, entry.Name(), err)
			}
		}
	}
	return nil
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
			ServiceState:            webServerServiceState(manager.Server.Running),
			ApplyStatus:             WebServerConfigApplyStatusSavedNotReloaded,
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
			return WebServerConfigUpdateResult{}, &WebServerConfigApplyError{
				Status: WebServerConfigApplyStatusReloadFailedRolled,
				Err:    fmt.Errorf("reload failed; previous content restored: %w", err),
			}
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
		ServiceState:            webServerServiceState(manager.Server.Running),
		ApplyStatus:             webServerConfigApplyStatus(reloaded),
	}, nil
}

func webServerServiceState(running bool) string {
	if running {
		return WebServerServiceStateRunning
	}
	return WebServerServiceStateNotRunning
}

func webServerConfigApplyStatus(reloaded bool) string {
	if reloaded {
		return WebServerConfigApplyStatusReloaded
	}
	return WebServerConfigApplyStatusSavedNotReloaded
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
	switch manager.Server.Component {
	case "apache":
		return manager.runCommand(timeoutCtx, manager.Server.BinaryPath, "-t", "-f", manager.Server.MainConfigPath)
	case "caddy":
		return manager.runCommand(timeoutCtx, manager.Server.BinaryPath, "validate", "--config", manager.Server.MainConfigPath, "--adapter", "caddyfile")
	}
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
	if manager.Server.Component == "apache" || manager.Server.Component == "caddy" {
		if strings.TrimSpace(manager.Server.ServiceName) == "" {
			return errors.New("Web server service name is not configured")
		}
		return manager.runCommand(timeoutCtx, "systemctl", "reload", manager.Server.ServiceName+".service")
	}
	return manager.run(timeoutCtx, "-s", "reload")
}

func (manager *WebServerConfigManager) run(ctx context.Context, args ...string) error {
	return manager.runCommand(ctx, manager.Server.BinaryPath, args...)
}

func (manager *WebServerConfigManager) runCommand(ctx context.Context, command string, args ...string) error {
	output, err := manager.Runner.Run(ctx, command, args...)
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	// Nginx/OpenResty report the syntax result in the command output. Some
	// wrappers can return a non-zero status even after reporting a successful
	// syntax check, so honor the explicit success markers for validation.
	lowerMessage := strings.ToLower(message)
	if strings.Contains(lowerMessage, "syntax is ok") ||
		strings.Contains(lowerMessage, "test is successful") {
		return nil
	}
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
	candidates = append(candidates, managedWebServerCandidates()...)
	if binary := strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER_BIN")); binary != "" {
		component := strings.ToLower(strings.TrimSpace(os.Getenv("ONEINSTACK_WEB_SERVER")))
		if component != "openresty" && component != "tengine" && component != "apache" && component != "caddy" {
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
		} else if component == "tengine" {
			name = "Tengine"
		} else if component == "apache" {
			name = "Apache HTTP Server"
		} else if component == "caddy" {
			name = "Caddy"
		}
		candidates = append(candidates, webServerCandidate{
			Component: component,
			Name:      name,
			Service:   webServiceName(component),
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
			Service:   webServiceName("nginx"),
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
			Service:   webServiceName("nginx"),
			Binary:    defaultNginxBinary,
			Prefix:    "/usr/local/nginx",
			Config:    "/usr/local/nginx/conf",
			Priority:  40,
		},
		webServerCandidate{
			Component: "openresty",
			Name:      "OpenResty",
			Service:   webServiceName("openresty"),
			Binary:    "/usr/local/openresty/nginx/sbin/nginx",
			Prefix:    "/usr/local/openresty/nginx",
			Config:    "/usr/local/openresty/nginx/conf",
			Priority:  40,
		},
		webServerCandidate{Component: "tengine", Name: "Tengine", Service: webServiceName("tengine"), Binary: "/usr/local/tengine/sbin/nginx", Prefix: "/usr/local/tengine", Config: "/usr/local/tengine/conf", Priority: 40},
		webServerCandidate{Component: "apache", Name: "Apache HTTP Server", Service: webServiceName("apache"), Binary: "/usr/local/apache/bin/httpd", Prefix: "/usr/local/apache", Config: "/usr/local/apache/conf", Priority: 40},
		webServerCandidate{Component: "caddy", Name: "Caddy", Service: webServiceName("caddy"), Binary: "/usr/local/caddy/bin/caddy", Prefix: "/usr/local/caddy", Config: "/usr/local/caddy/conf", Priority: 40},
		webServerCandidate{
			Component: "nginx",
			Name:      "Nginx",
			Service:   webServiceName("nginx"),
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
			Service:   webServiceName("nginx"),
			Binary:    "/usr/local/sbin/nginx",
			Prefix:    "/usr/local",
			Config:    "/usr/local/etc/nginx",
			Priority:  10,
		},
	)
	for _, executable := range []string{"openresty", "nginx", "tengine", "httpd", "caddy"} {
		if path, err := exec.LookPath(executable); err == nil {
			component := "nginx"
			name := "Nginx"
			service := webServiceName(component)
			prefix := "/etc/nginx"
			config := "/etc/nginx"
			if executable == "openresty" || strings.Contains(strings.ToLower(path), "openresty") {
				component = "openresty"
				name = "OpenResty"
				prefix = "/usr/local/openresty/nginx"
				config = filepath.Join(prefix, "conf")
				service = webServiceName(component)
			} else if executable == "tengine" || strings.Contains(strings.ToLower(path), "tengine") {
				component, name, service = "tengine", "Tengine", webServiceName("tengine")
				prefix, config = "/usr/local/tengine", "/usr/local/tengine/conf"
			} else if executable == "httpd" || strings.Contains(strings.ToLower(path), "apache") {
				component, name, service = "apache", "Apache HTTP Server", webServiceName("apache")
				prefix, config = "/usr/local/apache", "/usr/local/apache/conf"
			} else if executable == "caddy" || strings.Contains(strings.ToLower(path), "caddy") {
				component, name, service = "caddy", "Caddy", webServiceName("caddy")
				prefix, config = "/usr/local/caddy", "/usr/local/caddy/conf"
			} else if path == "/usr/sbin/nginx" {
				// Ubuntu/Debian package layout: binary in /usr/sbin,
				// runtime prefix in /usr/share/nginx, config in /etc/nginx.
				prefix = "/usr/share/nginx"
				config = "/etc/nginx"
			}
			candidates = append(candidates, webServerCandidate{
				Component: component,
				Name:      name,
				Service:   service,
				Binary:    path,
				Prefix:    prefix,
				Config:    config,
			})
		}
	}
	return candidates
}

func managedWebServerCandidates() []webServerCandidate {
	return managedWebServerCandidatesWithLookup(systemctlShowProperties)
}

func managedWebServerCandidatesWithLookup(lookup systemdShowLookup) []webServerCandidate {
	if lookup == nil {
		return nil
	}
	definitions := []struct {
		component string
		name      string
		priority  int
	}{
		{component: "nginx", name: "Nginx", priority: 160},
		{component: "openresty", name: "OpenResty", priority: 160},
		{component: "tengine", name: "Tengine", priority: 160},
		{component: "apache", name: "Apache HTTP Server", priority: 160},
		{component: "caddy", name: "Caddy", priority: 160},
	}
	candidates := make([]webServerCandidate, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		serviceName := webServiceName(definition.component)
		if serviceName == "" {
			continue
		}
		properties, err := lookup(serviceName+".service", "ExecStart")
		if err != nil {
			continue
		}
		binary := parseSystemdExecStartBinary(properties["ExecStart"])
		if binary == "" {
			continue
		}
		key := definition.component + "|" + binary
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		prefix, config := defaultManagedWebServerLayout(definition.component, binary)
		candidates = append(candidates, webServerCandidate{
			Component: definition.component,
			Name:      definition.name,
			Service:   serviceName,
			Binary:    binary,
			Prefix:    prefix,
			Config:    config,
			Priority:  definition.priority,
		})
	}
	return candidates
}

func systemctlShowProperties(unit string, properties ...string) (map[string]string, error) {
	if strings.TrimSpace(unit) == "" || len(properties) == 0 {
		return nil, errors.New("systemd unit properties are required")
	}
	args := []string{"show", unit}
	for _, property := range properties {
		property = strings.TrimSpace(property)
		if property == "" {
			continue
		}
		args = append(args, "--property="+property)
	}
	if len(args) == 2 {
		return nil, errors.New("systemd property list is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "systemctl", args...).Output()
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(properties))
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		values[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseSystemdExecStartBinary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if match := systemdExecPathPattern.FindStringSubmatch(value); len(match) == 2 {
		return filepath.Clean(match[1])
	}
	if match := systemdAbsPathPattern.FindStringSubmatch(value); len(match) == 2 {
		return filepath.Clean(match[1])
	}
	return ""
}

func defaultManagedWebServerLayout(component, binary string) (string, string) {
	binary = filepath.Clean(strings.TrimSpace(binary))
	switch strings.ToLower(strings.TrimSpace(component)) {
	case "nginx":
		if binary == "/usr/sbin/nginx" {
			return "/usr/share/nginx", "/etc/nginx"
		}
		if strings.HasSuffix(binary, "/sbin/nginx") {
			prefix := filepath.Dir(filepath.Dir(binary))
			return prefix, filepath.Join(prefix, "conf")
		}
	case "openresty":
		if strings.HasSuffix(binary, "/nginx/sbin/nginx") {
			prefix := filepath.Dir(filepath.Dir(binary))
			return prefix, filepath.Join(prefix, "conf")
		}
		if strings.HasSuffix(binary, "/bin/openresty") {
			root := filepath.Dir(filepath.Dir(binary))
			prefix := filepath.Join(root, "nginx")
			return prefix, filepath.Join(prefix, "conf")
		}
	case "tengine":
		if strings.HasSuffix(binary, "/sbin/nginx") {
			prefix := filepath.Dir(filepath.Dir(binary))
			return prefix, filepath.Join(prefix, "conf")
		}
	case "apache":
		if strings.HasSuffix(binary, "/bin/httpd") {
			prefix := filepath.Dir(filepath.Dir(binary))
			return prefix, filepath.Join(prefix, "conf")
		}
	case "caddy":
		if strings.HasSuffix(binary, "/bin/caddy") {
			prefix := filepath.Dir(binary)
			return prefix, "/etc/caddy"
		}
	}
	prefix := filepath.Dir(filepath.Dir(binary))
	if prefix == "." || prefix == "/" {
		prefix = filepath.Dir(binary)
	}
	return prefix, filepath.Join(prefix, "conf")
}

func installedWebServerComponents() map[string]bool {
	result := make(map[string]bool)
	if app.DB() == nil {
		return result
	}
	var rows []models.Software
	if err := app.DB().
		Select("component").
		Where("installed = ? AND component IN ?", true, []string{"nginx", "openresty", "tengine", "apache", "caddy"}).
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

// webServerIsRunning uses the same detected binary and configuration layout as
// the publisher. The process executable scan is fast, while the PID-file
// fallback keeps OpenResty detection correct when /proc executable links are
// unavailable to the Panel process.
func webServerIsRunning(candidate webServerCandidate, runningExecutables map[string]bool) bool {
	if runningExecutables[canonicalPath(filepath.Clean(candidate.Binary))] {
		return true
	}
	pidPath := filepath.Join(candidate.Prefix, "logs", "nginx.pid")
	mainConfig := filepath.Join(candidate.Config, "nginx.conf")
	if data, err := readBoundedFile(mainConfig, maxWebServerConfigBytes); err == nil {
		if configured, ok := nginxPIDPath(string(data), candidate.Prefix); ok {
			pidPath = configured
		}
	}
	data, err := readBoundedFile(pidPath, 64)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func nginxPIDPath(config, prefix string) (string, bool) {
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "pid ") || !strings.HasSuffix(line, ";") {
			continue
		}
		value := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "pid "), ";"))
		if value == "" || strings.ContainsAny(value, "\t ") {
			return "", false
		}
		if !filepath.IsAbs(value) {
			value = filepath.Join(prefix, value)
		}
		return filepath.Clean(value), true
	}
	return "", false
}

func inspectWebServerVersion(component, binary string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "-v").CombinedOutput()
	if err != nil && len(output) == 0 {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(component), "caddy") {
		match := caddyVersionPattern.FindStringSubmatch(string(output))
		if len(match) == 2 {
			return match[1]
		}
		return ""
	}
	match := webServerVersionPattern.FindStringSubmatch(string(output))
	for index := 1; index < len(match); index++ {
		if strings.TrimSpace(match[index]) != "" {
			return match[index]
		}
	}
	return ""
}

func webServiceName(component string) string {
	switch strings.ToLower(strings.TrimSpace(component)) {
	case "nginx":
		return "oneinstack-nginx"
	case "openresty":
		return "oneinstack-openresty"
	case "tengine":
		return "oneinstack-tengine"
	case "caddy":
		return "oneinstack-caddy"
	case "apache":
		return "oneinstack-httpd"
	default:
		return ""
	}
}

func managedVhostDir(component string) string {
	root := strings.TrimSpace(app.ONE_CONFIG.System.WebVhostRoot)
	if root == "" {
		root = filepath.Join(app.GetBasePath(), "vhost")
	}
	return filepath.Join(filepath.Clean(root), strings.ToLower(strings.TrimSpace(component)))
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
	return resolveManagedConfigPathWithOptions(root, relativePath, false)
}

func resolveManagedConfigPathForPreview(root, relativePath string) (string, string, error) {
	return resolveManagedConfigPathWithOptions(root, relativePath, true)
}

func resolveManagedConfigPathWithOptions(root, relativePath string, allowMissing bool) (string, string, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return "", "", errors.New("configuration root is invalid")
	}
	relativePath = filepath.FromSlash(strings.TrimSpace(relativePath))
	cleaned := filepath.Clean(relativePath)
	if cleaned == "." || filepath.IsAbs(cleaned) ||
		cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) ||
		(!strings.EqualFold(filepath.Ext(cleaned), ".conf") &&
			!strings.EqualFold(filepath.Base(cleaned), "Caddyfile")) {
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
			if allowMissing && errors.Is(err, os.ErrNotExist) {
				break
			}
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
