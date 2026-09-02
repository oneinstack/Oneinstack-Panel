package container

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"oneinstack/app"
	"oneinstack/internal/models"

	"gopkg.in/yaml.v3"
)

const (
	composeContentLimit = 2 << 20
	composeBackupLimit  = 10
)

var (
	ErrComposeUnavailable       = errors.New("docker compose unavailable")
	ErrComposeConfigInvalid     = errors.New("compose configuration invalid")
	ErrComposeProjectNotFound   = errors.New("compose project not found")
	ErrComposeProjectConflict   = errors.New("compose project conflict")
	ErrComposeProjectBusy       = errors.New("compose project busy")
	ErrComposePreviewStale      = errors.New("compose preview is stale")
	ErrComposeMultiFile         = errors.New("compose multi-file edit unsupported")
	ErrComposeOperationFailed   = errors.New("compose operation failed")
	ErrComposeOperationTimeout  = errors.New("compose operation timed out")
	ErrComposeConfigUnavailable = errors.New("compose configuration unavailable")
	ErrComposeTemplateNotFound  = errors.New("compose template not found")
)

var composeProjectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var composeServiceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type ComposeTaskRequest struct {
	ProjectName    string               `json:"projectName"`
	Target         ComposeProjectTarget `json:"-"`
	ContentPath    string               `json:"contentPath,omitempty"`
	ContentHash    string               `json:"contentHash,omitempty"`
	PreviewHash    string               `json:"previewHash,omitempty"`
	RemoveVolumes  bool                 `json:"removeVolumes,omitempty"`
	ManagedProject bool                 `json:"managedProject,omitempty"`
}

type ComposeProjectTarget struct {
	ProjectName string   `json:"projectName"`
	Status      string   `json:"status,omitempty"`
	ConfigFiles []string `json:"configFiles"`
	WorkingDir  string   `json:"workingDir"`
	Managed     bool     `json:"managed"`
}

type ComposeServiceSummary struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	Build bool   `json:"build,omitempty"`
}

type ComposeConfigSummary struct {
	Services []ComposeServiceSummary `json:"services"`
	Ports    []string                `json:"ports,omitempty"`
	Networks []string                `json:"networks,omitempty"`
	Volumes  []string                `json:"volumes,omitempty"`
	Warnings []string                `json:"warnings,omitempty"`
}

type ComposePreview struct {
	Action             string               `json:"action"`
	ProjectName        string               `json:"projectName"`
	PreviewFingerprint string               `json:"previewFingerprint"`
	Summary            ComposeConfigSummary `json:"summary"`
	Target             ComposeProjectTarget `json:"-"`
	EffectiveContent   string               `json:"-"`
	Impact             string               `json:"impact"`
}

type ComposeProjectDetail struct {
	ComposeProjectTarget
	ConfigReadable bool                  `json:"configReadable"`
	Editable       bool                  `json:"editable"`
	Services       []map[string]any      `json:"services"`
	ConfigSummary  *ComposeConfigSummary `json:"configSummary,omitempty"`
	EditReason     string                `json:"editReason,omitempty"`
	SafetyTips     []string              `json:"safetyTips,omitempty"`
}

// ComposeSensitiveField identifies a sensitive YAML field without returning
// its value. Paths are informational only; the server must never trust them
// as file locations or update targets.
type ComposeSensitiveField struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// ComposeConfigView is the contract used by the config editor. The default
// view is redacted; plaintext is returned only by the explicit reveal path
// after the handler has re-authenticated the operator.
type ComposeConfigView struct {
	ProjectName        string                  `json:"projectName"`
	ConfigFile         string                  `json:"configFile"`
	Content            string                  `json:"content"`
	ContentMode        string                  `json:"contentMode"`
	ContainsSensitive  bool                    `json:"containsSensitiveConfig"`
	SensitiveFields    []ComposeSensitiveField `json:"sensitiveFields,omitempty"`
	ConfigRevision     string                  `json:"configRevision"`
	RedactionAvailable bool                    `json:"redactionAvailable"`
	RedactionReason    string                  `json:"redactionReason,omitempty"`
}

type ComposeLogOptions struct {
	Tail       int
	Since      string
	Until      string
	Timestamps bool
	Service    string
}

type ComposeError struct {
	Kind    error
	Message string
}

const composeRedactedSecretMarker = "[已隐藏]"

var (
	composeSensitiveKeyPattern = regexp.MustCompile(`(?i)(^|[_.-])(?:password|passwd|pwd|pass|passphrase|secret|token|cookie|auth|authorization|api[-_]?keys?|access[-_]?keys?|private[-_]?keys?|encryption[-_]?key|signing[-_]?key|ssh[-_]?key|master[-_]?key|client[-_]?secrets?|credentials?|auth[-_]?tokens?|session[-_]?tokens?|dsn|connection[-_]?strings?|database[-_]?url|jdbc[-_]?url|redis[-_]?url|mongodb[-_]?uri)(?:$|[_.-])`)
	composeSecretURLPattern    = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@[:space:]:]+):([^/@[:space:]]+)(@)`)
	composeSecretFlagPattern   = regexp.MustCompile(`(?i)(--?(?:password|passwd|pwd|passphrase|secret|token|cookie|authorization|api[-_]?keys?|access[-_]?keys?|private[-_]?keys?|client[-_]?secrets?|credentials?|auth[-_]?tokens?|session[-_]?tokens?|requirepass)(?:=|[[:space:]]+))("[^"\r\n]*"|'[^'\r\n]*'|[^[:space:],;}\]]+)`)
)

func (e *ComposeError) Error() string { return e.Message }
func (e *ComposeError) Unwrap() error { return e.Kind }

func composeError(kind error, message string) error {
	return &ComposeError{Kind: kind, Message: message}
}

func isComposeOperation(operation string) bool {
	return strings.HasPrefix(strings.TrimSpace(operation), "compose.")
}

func validateComposeProjectName(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !composeProjectNamePattern.MatchString(value) {
		return "", composeError(ErrComposeConfigInvalid, "Compose 项目名称无效，只能包含字母、数字、点、下划线和短横线")
	}
	return value, nil
}

func validateComposeServiceName(value string) error {
	if !composeServiceNamePattern.MatchString(strings.TrimSpace(value)) {
		return composeError(ErrComposeConfigInvalid, "Compose 服务名称无效")
	}
	return nil
}

func validateComposeOperation(operation string) error {
	switch operation {
	case models.ContainerTaskOperationComposeCreate,
		models.ContainerTaskOperationComposeEdit,
		models.ContainerTaskOperationComposeStart,
		models.ContainerTaskOperationComposeStop,
		models.ContainerTaskOperationComposeRestart,
		models.ContainerTaskOperationComposeUpdate,
		models.ContainerTaskOperationComposeDelete:
		return nil
	default:
		return composeError(ErrComposeConfigInvalid, "不支持的 Compose 操作")
	}
}

func (s *Service) ensureComposeAvailable(ctx context.Context) error {
	runtime := s.Runtime(ctx)
	if !runtime.Available {
		return composeError(ErrRuntimeUnavailable, "Docker 运行时不可用")
	}
	if strings.TrimSpace(runtime.ComposeVersion) == "" {
		return composeError(ErrComposeUnavailable, "Docker Compose 插件不可用")
	}
	return nil
}

func composeManagedRoot() string {
	return filepath.Clean(filepath.Join(app.GetBasePath(), "compose"))
}

func composeBackupRoot() string {
	return filepath.Clean(filepath.Join(app.GetBasePath(), "backups", "compose"))
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func managedComposeTarget(name string) ComposeProjectTarget {
	root := filepath.Join(composeManagedRoot(), name)
	return ComposeProjectTarget{
		ProjectName: name,
		ConfigFiles: []string{filepath.Join(root, "compose.yml")},
		WorkingDir:  root,
		Managed:     true,
	}
}

func ensureComposeWorkingDir(target ComposeProjectTarget) error {
	if target.WorkingDir == "" || !filepath.IsAbs(target.WorkingDir) {
		return composeError(ErrComposeConfigInvalid, "Compose 项目工作目录不可用")
	}
	if target.Managed {
		root := composeManagedRoot()
		rootInfo, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(root, 0700); err != nil {
				return composeError(ErrComposeConfigInvalid, "无法准备 Compose 项目目录")
			}
			rootInfo, err = os.Lstat(root)
		}
		if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() || !pathWithin(root, target.WorkingDir) {
			return composeError(ErrComposeConfigInvalid, "Compose 项目目录不在面板管理范围内")
		}
	}
	if err := os.MkdirAll(target.WorkingDir, 0700); err != nil {
		return composeError(ErrComposeConfigInvalid, "无法准备 Compose 项目目录")
	}
	info, err := os.Lstat(target.WorkingDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return composeError(ErrComposeConfigInvalid, "Compose 项目目录不是安全目录")
	}
	return nil
}

func parseComposeStringField(item map[string]any, key string) string {
	for candidate, value := range item {
		if strings.EqualFold(candidate, key) {
			if text, ok := value.(string); ok {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func parseComposeFiles(value any, workingDir string) []string {
	var values []string
	switch typed := value.(type) {
	case string:
		values = strings.FieldsFunc(typed, func(r rune) bool { return r == ',' || r == '\n' })
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case []string:
		values = append(values, typed...)
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || value == "--" {
			continue
		}
		absolute := value
		if !filepath.IsAbs(absolute) && workingDir != "" {
			absolute = filepath.Join(workingDir, absolute)
		}
		absolute, err := filepath.Abs(absolute)
		if err != nil {
			continue
		}
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		result = append(result, absolute)
	}
	return result
}

func composeTargetFromItem(item map[string]any) (ComposeProjectTarget, bool) {
	name := parseComposeStringField(item, "Name")
	if name == "" {
		return ComposeProjectTarget{}, false
	}
	name, err := validateComposeProjectName(name)
	if err != nil {
		return ComposeProjectTarget{}, false
	}
	workingDir := parseComposeStringField(item, "WorkingDir")
	if workingDir == "--" {
		workingDir = ""
	}
	if workingDir != "" {
		if absolute, err := filepath.Abs(workingDir); err == nil {
			workingDir = absolute
		}
	}
	files := []string{}
	for key, value := range item {
		if strings.EqualFold(key, "ConfigFiles") {
			files = parseComposeFiles(value, workingDir)
			break
		}
	}
	if workingDir == "" && len(files) > 0 {
		workingDir = filepath.Dir(files[0])
	}
	return ComposeProjectTarget{
		ProjectName: name,
		Status:      parseComposeStringField(item, "Status"),
		ConfigFiles: files,
		WorkingDir:  workingDir,
		Managed:     false,
	}, true
}

func (s *Service) composeRuntimeProjects(ctx context.Context) ([]ComposeProjectTarget, error) {
	if err := s.ensureComposeAvailable(ctx); err != nil {
		return nil, err
	}
	items, err := s.listComposeRaw(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]ComposeProjectTarget, 0, len(items))
	for _, item := range items {
		if target, ok := composeTargetFromItem(item); ok {
			projects = append(projects, target)
		}
	}
	return projects, nil
}

func (s *Service) listComposeRaw(ctx context.Context) ([]map[string]any, error) {
	out, err := s.run(ctx, "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return []map[string]any{}, nil
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
		if items == nil {
			return []map[string]any{}, nil
		}
		return items, nil
	}
	items = make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, composeError(ErrComposeOperationFailed, "Docker Compose 项目列表格式无效")
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, composeError(ErrComposeOperationFailed, "Docker Compose 项目列表读取失败")
	}
	return items, nil
}

func (s *Service) ListComposeProjects(ctx context.Context) ([]map[string]any, error) {
	projects, err := s.composeRuntimeProjects(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]ComposeProjectTarget, len(projects))
	for _, project := range projects {
		byName[project.ProjectName] = project
	}
	root := composeManagedRoot()
	entries, readErr := os.ReadDir(root)
	if readErr == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name, nameErr := validateComposeProjectName(entry.Name())
			if nameErr != nil {
				continue
			}
			candidate := managedComposeTarget(name)
			if _, statErr := os.Stat(candidate.ConfigFiles[0]); statErr != nil {
				continue
			}
			if current, exists := byName[name]; exists {
				if isManagedComposeTarget(current) {
					current.Managed = true
					byName[name] = current
				}
			} else {
				byName[name] = candidate
			}
		}
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		project := byName[name]
		configReadable, editable, editReason := composeConfigState(project)
		safetyTips := composeProjectSafetyTips(project, configReadable)
		status := project.Status
		if status == "" {
			status = "not_created"
		}
		actions := []string{"start", "stop", "restart", "update", "logs", "delete"}
		if editable {
			actions = append([]string{"edit"}, actions...)
		}
		items = append(items, map[string]any{
			"Name":           project.ProjectName,
			"Status":         status,
			"ConfigFiles":    strings.Join(project.ConfigFiles, ","),
			"WorkingDir":     project.WorkingDir,
			"projectName":    project.ProjectName,
			"managed":        project.Managed,
			"configReadable": configReadable,
			"editable":       editable,
			"editReason":     editReason,
			"safetyTips":     safetyTips,
			"actions":        actions,
		})
	}
	return items, nil
}

func composeProjectSafetyTips(target ComposeProjectTarget, configReadable bool) []string {
	tips := []string{
		"删除 Compose 项目不会自动删除镜像或宿主机绑定目录",
		"删除 Compose 声明的卷需要单独确认并具备存储卷管理权限",
	}
	if target.Managed {
		tips = append(tips, "删除面板创建的项目时仅移除面板管理的 compose.yml")
	} else {
		tips = append(tips, "删除外部项目时保留原始 Compose 配置文件")
	}
	if configReadable && len(target.ConfigFiles) == 1 {
		if content, err := os.ReadFile(target.ConfigFiles[0]); err == nil {
			if summary, summaryErr := validateComposeContent(string(content), ""); summaryErr == nil {
				tips = append(tips, summary.Warnings...)
			}
		}
	}
	return tips
}

func isManagedComposeTarget(target ComposeProjectTarget) bool {
	managed := managedComposeTarget(target.ProjectName)
	if target.WorkingDir == managed.WorkingDir {
		return true
	}
	for _, path := range target.ConfigFiles {
		if filepath.Clean(path) == filepath.Clean(managed.ConfigFiles[0]) {
			return true
		}
	}
	return false
}

func (s *Service) ResolveComposeProject(ctx context.Context, name string) (ComposeProjectTarget, error) {
	name, err := validateComposeProjectName(name)
	if err != nil {
		return ComposeProjectTarget{}, err
	}
	projects, err := s.composeRuntimeProjects(ctx)
	if err != nil {
		return ComposeProjectTarget{}, err
	}
	for _, project := range projects {
		if project.ProjectName == name {
			project.Managed = isManagedComposeTarget(project)
			return project, nil
		}
	}
	candidate := managedComposeTarget(name)
	if _, statErr := os.Stat(candidate.ConfigFiles[0]); statErr == nil {
		return candidate, nil
	}
	return ComposeProjectTarget{}, composeError(ErrComposeProjectNotFound, "Compose 项目不存在")
}

func (s *Service) ComposeProjectDetail(ctx context.Context, name string) (ComposeProjectDetail, error) {
	target, err := s.ResolveComposeProject(ctx, name)
	if err != nil {
		return ComposeProjectDetail{}, err
	}
	configReadable, editable, editReason := composeConfigState(target)
	services := []map[string]any{}
	if configReadable {
		services, err = s.composeJSON(ctx, target, "ps", "--all", "--format", "json")
		if err != nil {
			return ComposeProjectDetail{}, err
		}
	}
	detail := ComposeProjectDetail{ComposeProjectTarget: target, ConfigReadable: configReadable, Editable: editable, Services: services, EditReason: editReason, SafetyTips: composeProjectSafetyTips(target, configReadable)}
	if configReadable && len(target.ConfigFiles) > 0 {
		content, readErr := os.ReadFile(target.ConfigFiles[0])
		if readErr == nil {
			if summary, summaryErr := validateComposeContent(string(content), target.WorkingDir); summaryErr == nil {
				detail.ConfigSummary = &summary
			}
		}
	}
	return detail, nil
}

func composeConfigState(target ComposeProjectTarget) (bool, bool, string) {
	if len(target.ConfigFiles) == 0 {
		return false, false, "Compose 项目没有可读取的配置文件"
	}
	for _, path := range target.ConfigFiles {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return false, false, "Compose 配置文件不可读取"
		}
	}
	if len(target.ConfigFiles) != 1 {
		return true, false, "当前项目使用多个 Compose 配置文件，暂不支持单 YAML 编辑"
	}
	info, err := os.Lstat(target.ConfigFiles[0])
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return true, false, "为避免越权覆盖，符号链接配置文件不可编辑"
	}
	return true, true, ""
}

func validateComposeContent(content, workingDir string) (ComposeConfigSummary, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > composeContentLimit || !utf8.ValidString(content) {
		return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "Compose YAML 不能为空、必须是 UTF-8，且不能超过 2 MiB")
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(content), &document); err != nil || document == nil {
		return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "Compose YAML 格式无效，请检查缩进和字段结构")
	}
	servicesValue, exists := document["services"]
	if !exists {
		return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "Compose YAML 必须包含 services")
	}
	services, ok := servicesValue.(map[string]any)
	if !ok || len(services) == 0 {
		return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "Compose services 必须是非空对象")
	}

	summary := ComposeConfigSummary{Services: make([]ComposeServiceSummary, 0, len(services))}
	for name, raw := range services {
		if err := validateComposeServiceName(name); err != nil {
			return ComposeConfigSummary{}, err
		}
		service, ok := raw.(map[string]any)
		if !ok {
			return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "Compose 服务 "+name+" 必须是对象")
		}
		item := ComposeServiceSummary{Name: name}
		if image, ok := service["image"].(string); ok {
			item.Image = strings.TrimSpace(image)
		}
		if _, ok := service["build"]; ok {
			item.Build = true
		}
		summary.Services = append(summary.Services, item)
		if isOfficialPostgres18OrNewerImage(item.Image) && composeHasLegacyPostgresDataMount(service["volumes"]) {
			return ComposeConfigSummary{}, composeError(ErrComposeConfigInvalid, "服务 "+name+" 使用了 PostgreSQL 18+ 的旧数据目录挂载；"+postgresDataMountRecommendation())
		}
		if privileged, ok := service["privileged"].(bool); ok && privileged {
			summary.Warnings = append(summary.Warnings, "服务 "+name+" 启用了 privileged，具有较高宿主机访问风险")
		}
		if networkMode, ok := service["network_mode"].(string); ok && strings.EqualFold(strings.TrimSpace(networkMode), "host") {
			summary.Warnings = append(summary.Warnings, "服务 "+name+" 使用 host 网络模式")
		}
		if ports, ok := service["ports"].([]any); ok {
			for _, port := range ports {
				if text, ok := composePortSummary(port); ok {
					summary.Ports = append(summary.Ports, text)
				}
			}
		}
		if composeIsRedisService(name, service) {
			if !composeRedisAuthenticationConfigured(service) {
				summary.Warnings = append(summary.Warnings, "服务 "+name+" 未检测到 Redis 认证配置，可能无需密码即可访问；请设置 requirepass 或 ACL 认证")
			}
			if composeRedisNetworkExposed(service) {
				summary.Warnings = append(summary.Warnings, "服务 "+name+" 发布了 Redis 端口且未限制为本机回环地址，存在网络暴露风险；请绑定 127.0.0.1/::1，或配置受控网络和防火墙")
			}
		}
	}
	sort.Slice(summary.Services, func(i, j int) bool { return summary.Services[i].Name < summary.Services[j].Name })
	sort.Strings(summary.Warnings)
	summary.Networks = mapKeys(document["networks"])
	summary.Volumes = mapKeys(document["volumes"])
	if workingDir != "" {
		if err := validateComposeDependencies(document, workingDir); err != nil {
			return ComposeConfigSummary{}, err
		}
	}
	return summary, nil
}

func composeHasLegacyPostgresDataMount(value any) bool {
	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case []string:
		for _, item := range typed {
			items = append(items, item)
		}
	default:
		return false
	}

	for _, item := range items {
		target := ""
		switch volume := item.(type) {
		case string:
			parts := strings.Split(volume, ":")
			for index := 1; index < len(parts); index++ {
				candidate := strings.TrimSpace(parts[index])
				if strings.HasPrefix(candidate, "/") {
					target = candidate
					break
				}
			}
		case map[string]any:
			target = composeScalarString(volume["target"])
		}
		if isLegacyPostgresDataMountTarget(target) {
			return true
		}
	}
	return false
}

func composeIsRedisService(name string, service map[string]any) bool {
	image := strings.ToLower(strings.TrimSpace(composeScalarString(service["image"])))
	if image != "" {
		if at := strings.IndexByte(image, '@'); at >= 0 {
			image = image[:at]
		}
		if slash := strings.LastIndexByte(image, '/'); slash >= 0 {
			image = image[slash+1:]
		}
		if colon := strings.LastIndexByte(image, ':'); colon >= 0 {
			image = image[:colon]
		}
		if image == "redis" || strings.HasPrefix(image, "redis-") || image == "redisgraph" {
			return true
		}
	}

	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	return name == "redis" || strings.HasPrefix(name, "redis-") || strings.HasSuffix(name, "-redis")
}

func composeRedisAuthenticationConfigured(service map[string]any) bool {
	if composeRedisEnvironmentAuthenticationConfigured(service["environment"]) {
		return true
	}
	return composeRedisCommandAuthenticationConfigured(service["command"]) || composeRedisCommandAuthenticationConfigured(service["entrypoint"])
}

func composeRedisEnvironmentAuthenticationConfigured(value any) bool {
	switch environment := value.(type) {
	case map[string]any:
		for key, rawValue := range environment {
			key = strings.ToUpper(strings.TrimSpace(key))
			switch key {
			case "REDIS_PASSWORD", "REDIS_PASSWORD_FILE", "REDIS_ACLFILE":
				if composeScalarString(rawValue) != "" {
					return true
				}
			case "REDIS_ARGS":
				if composeRedisCommandAuthenticationConfigured(rawValue) {
					return true
				}
			}
		}
	case []any:
		for _, item := range environment {
			if composeRedisEnvironmentEntryAuthenticationConfigured(composeScalarString(item)) {
				return true
			}
		}
	case []string:
		for _, item := range environment {
			if composeRedisEnvironmentEntryAuthenticationConfigured(item) {
				return true
			}
		}
	}
	return false
}

func composeRedisEnvironmentEntryAuthenticationConfigured(value string) bool {
	key, rawValue, hasValue := strings.Cut(strings.TrimSpace(value), "=")
	key = strings.ToUpper(strings.TrimSpace(key))
	if key == "REDIS_ARGS" {
		return hasValue && composeRedisCommandAuthenticationConfigured(rawValue)
	}
	if key != "REDIS_PASSWORD" && key != "REDIS_PASSWORD_FILE" && key != "REDIS_ACLFILE" {
		return false
	}
	return hasValue && strings.TrimSpace(rawValue) != ""
}

func composeRedisCommandAuthenticationConfigured(value any) bool {
	var parts []string
	switch command := value.(type) {
	case string:
		parts = []string{command}
	case []any:
		for _, item := range command {
			parts = append(parts, composeScalarString(item))
		}
	case []string:
		parts = append(parts, command...)
	default:
		return false
	}

	fields := strings.Fields(strings.Join(parts, " "))
	for index, field := range fields {
		field = strings.Trim(field, "\"'")
		for _, option := range []string{"--requirepass", "--aclfile"} {
			if field == option {
				return index+1 < len(fields) && strings.Trim(fields[index+1], "\"'") != ""
			}
			if strings.HasPrefix(field, option+"=") {
				return strings.Trim(strings.TrimPrefix(field, option+"="), "\"'") != ""
			}
		}
		if field == "--user" || strings.HasPrefix(field, "--user=") {
			return true
		}
	}
	return false
}

func composeRedisNetworkExposed(service map[string]any) bool {
	if networkMode, ok := service["network_mode"].(string); ok && strings.EqualFold(strings.TrimSpace(networkMode), "host") {
		return true
	}

	ports, ok := service["ports"].([]any)
	if !ok {
		if values, ok := service["ports"].([]string); ok {
			for _, port := range values {
				if !composePortHostLoopback(port) {
					return true
				}
			}
		}
		return false
	}
	for _, port := range ports {
		if !composePortHostLoopback(port) {
			return true
		}
	}
	return false
}

func composePortHostLoopback(value any) bool {
	hostIP := ""
	switch port := value.(type) {
	case map[string]any:
		hostIP = composeScalarString(port["host_ip"])
	case string:
		text := strings.TrimSpace(strings.SplitN(port, "/", 2)[0])
		if strings.HasPrefix(text, "[") {
			if end := strings.IndexByte(text, ']'); end > 0 {
				hostIP = text[1:end]
			}
		} else {
			parts := strings.Split(text, ":")
			if len(parts) >= 3 {
				hostIP = parts[0]
			}
		}
	}
	hostIP = strings.ToLower(strings.TrimSpace(strings.Trim(hostIP, "[]")))
	return hostIP == "127.0.0.1" || hostIP == "::1" || hostIP == "localhost"
}

func mapKeys(value any) []string {
	items, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for key := range items {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func composePortSummary(value any) (string, bool) {
	switch port := value.(type) {
	case string:
		text := strings.TrimSpace(port)
		return text, text != ""
	case map[string]any:
		target := composeScalarString(port["target"])
		if target == "" {
			return "", false
		}
		published := composeScalarString(port["published"])
		result := target
		if published != "" {
			result = published + ":" + target
		}
		if hostIP := composeScalarString(port["host_ip"]); hostIP != "" {
			result = hostIP + ":" + result
		}
		if protocol := composeScalarString(port["protocol"]); protocol != "" && !strings.EqualFold(protocol, "tcp") {
			result += "/" + protocol
		}
		return result, true
	default:
		return "", false
	}
}

func composeScalarString(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return strings.TrimSpace(fmt.Sprint(value))
	default:
		return ""
	}
}

func validateComposeDependencies(document map[string]any, workingDir string) error {
	services, _ := document["services"].(map[string]any)
	for _, raw := range services {
		service, _ := raw.(map[string]any)
		if build, ok := service["build"]; ok {
			contextPath := ""
			switch value := build.(type) {
			case string:
				contextPath = value
			case map[string]any:
				contextPath, _ = value["context"].(string)
			}
			if contextPath != "" {
				if err := requireComposePath(workingDir, contextPath, "build context"); err != nil {
					return err
				}
			}
		}
		if envFile, ok := service["env_file"]; ok {
			values := []string{}
			switch value := envFile.(type) {
			case string:
				values = []string{value}
			case []any:
				for _, item := range value {
					if text, ok := item.(string); ok {
						values = append(values, text)
					}
				}
			}
			for _, value := range values {
				if err := requireComposePath(workingDir, value, "env_file"); err != nil {
					return err
				}
			}
		}
		if volumes, ok := service["volumes"]; ok {
			if err := validateComposeVolumeDependencies(volumes, workingDir); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateComposeVolumeDependencies(value any, workingDir string) error {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	for _, item := range items {
		switch volume := item.(type) {
		case string:
			source := composeVolumeSource(volume)
			if source != "" && composeVolumeSourceIsPath(source) {
				if err := requireComposePath(workingDir, source, "volume mount"); err != nil {
					return err
				}
			}
		case map[string]any:
			volumeType, _ := volume["type"].(string)
			if strings.EqualFold(strings.TrimSpace(volumeType), "bind") {
				source, _ := volume["source"].(string)
				if err := requireComposePath(workingDir, source, "volume mount"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func composeVolumeSource(value string) string {
	value = strings.TrimSpace(value)
	separator := strings.IndexByte(value, ':')
	if separator <= 0 {
		return ""
	}
	return strings.TrimSpace(value[:separator])
}

func composeVolumeSourceIsPath(value string) bool {
	value = strings.TrimSpace(value)
	return filepath.IsAbs(value) || value == "." || value == ".." || strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../")
}

func requireComposePath(workingDir, value, label string) error {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		if value == "" {
			return composeError(ErrComposeConfigInvalid, label+" 不能为空")
		}
		if _, err := os.Stat(value); err != nil {
			return composeError(ErrComposeConfigInvalid, label+" 不存在")
		}
		return nil
	}
	path := filepath.Clean(filepath.Join(workingDir, value))
	if !pathWithin(workingDir, path) {
		return composeError(ErrComposeConfigInvalid, label+" 不能逃逸项目目录")
	}
	if _, err := os.Stat(path); err != nil {
		return composeError(ErrComposeConfigInvalid, label+" 不存在")
	}
	return nil
}

func (s *Service) ComposePreview(ctx context.Context, action, name, content string, removeVolumes bool) (ComposePreview, error) {
	if err := validateComposeOperation(composeOperationForAction(action)); err != nil {
		return ComposePreview{}, err
	}
	if err := s.ensureComposeAvailable(ctx); err != nil {
		return ComposePreview{}, err
	}
	name, err := validateComposeProjectName(name)
	if err != nil {
		return ComposePreview{}, err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	var target ComposeProjectTarget
	switch action {
	case "create":
		if _, resolveErr := s.ResolveComposeProject(ctx, name); resolveErr == nil {
			return ComposePreview{}, composeError(ErrComposeProjectConflict, "Compose 项目名称已存在")
		} else if !errors.Is(resolveErr, ErrComposeProjectNotFound) {
			return ComposePreview{}, resolveErr
		}
		target = managedComposeTarget(name)
	case "edit", "update", "delete":
		target, err = s.ResolveComposeProject(ctx, name)
		if err != nil {
			return ComposePreview{}, err
		}
	default:
		return ComposePreview{}, composeError(ErrComposeConfigInvalid, "该操作不需要 Compose 预览")
	}
	summary := ComposeConfigSummary{}
	effectiveContent := content
	fingerprintContent := effectiveContent
	if action == "create" || action == "edit" {
		if action == "edit" {
			_, editable, reason := composeConfigState(target)
			if !editable {
				if reason == "" {
					reason = "当前 Compose 配置不可编辑"
				}
				return ComposePreview{}, composeError(ErrComposeMultiFile, reason)
			}
			effectiveContent, err = s.restoreComposeRedactedContent(target, content)
			if err != nil {
				return ComposePreview{}, err
			}
		}
		var summaryErr error
		if summary, summaryErr = validateComposeContent(effectiveContent, target.WorkingDir); summaryErr != nil {
			return ComposePreview{}, summaryErr
		}
		if err := s.validateComposeCandidate(ctx, target, effectiveContent); err != nil {
			return ComposePreview{}, err
		}
		fingerprintContent = effectiveContent
	} else if len(target.ConfigFiles) > 0 {
		current, readErr := os.ReadFile(target.ConfigFiles[0])
		if readErr != nil {
			return ComposePreview{}, composeError(ErrComposeConfigUnavailable, "Compose 配置文件不可读取")
		}
		var summaryErr error
		workingDir := target.WorkingDir
		if action == "delete" {
			workingDir = ""
		}
		if summary, summaryErr = validateComposeContent(string(current), workingDir); summaryErr != nil {
			return ComposePreview{}, summaryErr
		}
		fingerprintContent = string(current)
	}
	fingerprint := composeFingerprint(action, name, target, fingerprintContent, removeVolumes)
	impact := composeImpactDescription(action, name, summary, removeVolumes)
	return ComposePreview{Action: action, ProjectName: name, PreviewFingerprint: fingerprint, Summary: summary, Target: target, EffectiveContent: effectiveContent, Impact: impact}, nil
}

func composeImpactDescription(action, projectName string, summary ComposeConfigSummary, removeVolumes bool) string {
	services := make([]string, 0, len(summary.Services))
	for _, service := range summary.Services {
		if name := strings.TrimSpace(service.Name); name != "" {
			services = append(services, name)
		}
	}

	networks := append([]string(nil), summary.Networks...)
	if len(networks) == 0 && len(services) > 0 {
		networks = []string{projectName + "_default"}
	}

	list := func(items []string) string {
		if len(items) == 0 {
			return "无"
		}
		return strings.Join(items, "、")
	}

	parts := make([]string, 0, 6)
	switch action {
	case "create":
		parts = append(parts, "创建容器："+list(services))
		parts = append(parts, "创建网络："+list(networks))
		parts = append(parts, "创建或使用卷："+list(summary.Volumes))
		parts = append(parts, "发布端口："+list(summary.Ports))
		parts = append(parts, "写入 Compose 配置并启动服务")
	case "edit":
		parts = append(parts, "更新 Compose 配置文件")
		parts = append(parts, "涉及容器服务："+list(services))
		parts = append(parts, "涉及网络："+list(networks))
		parts = append(parts, "涉及卷："+list(summary.Volumes))
		parts = append(parts, "涉及端口："+list(summary.Ports))
		parts = append(parts, "不会自动创建、重建或重启容器")
	case "update":
		parts = append(parts, "更新容器："+list(services))
		parts = append(parts, "使用或创建网络："+list(networks))
		parts = append(parts, "使用或创建卷："+list(summary.Volumes))
		parts = append(parts, "保持端口映射："+list(summary.Ports))
		parts = append(parts, "拉取镜像并重建、重启服务")
	case "delete":
		parts = append(parts, "删除容器："+list(services))
		parts = append(parts, "删除网络："+list(networks))
		if removeVolumes {
			parts = append(parts, "删除卷："+list(summary.Volumes))
		} else {
			parts = append(parts, "保留卷："+list(summary.Volumes))
		}
		parts = append(parts, "释放端口："+list(summary.Ports))
	}
	return strings.Join(parts, "；")
}

func composeOperationForAction(action string) string {
	return "compose." + strings.ToLower(strings.TrimSpace(action))
}

func composeFingerprint(action, name string, target ComposeProjectTarget, content string, removeVolumes bool) string {
	value := struct {
		Action        string               `json:"action"`
		Name          string               `json:"name"`
		Target        ComposeProjectTarget `json:"target"`
		ContentHash   string               `json:"contentHash"`
		RemoveVolumes bool                 `json:"removeVolumes"`
	}{Action: action, Name: name, Target: target, ContentHash: hashContent(content), RemoveVolumes: removeVolumes}
	data, _ := json.Marshal(value)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func hashContent(content string) string {
	digest := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(digest[:])
}

func (s *Service) validateComposeCandidate(ctx context.Context, target ComposeProjectTarget, content string) error {
	return s.withComposeCandidate(target, content, func(candidate ComposeProjectTarget) error {
		_, err := s.composeRun(ctx, candidate, "config", "--quiet")
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrComposeOperationTimeout) || errors.Is(err, ErrRuntimeUnavailable) || errors.Is(err, ErrComposeUnavailable) {
			return err
		}
		return composeError(ErrComposeConfigInvalid, "Compose 配置未通过 Docker Compose 校验")
	})
}

func (s *Service) withComposeCandidate(target ComposeProjectTarget, content string, action func(ComposeProjectTarget) error) error {
	if err := ensureComposeWorkingDir(target); err != nil {
		return err
	}
	file, err := os.CreateTemp(target.WorkingDir, ".oneinstack-compose-preview-*.yml")
	if err != nil {
		return composeError(ErrComposeConfigInvalid, "无法创建 Compose 预览文件")
	}
	path := file.Name()
	defer os.Remove(path)
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return composeError(ErrComposeConfigInvalid, "无法设置 Compose 预览文件权限")
	}
	if _, err := file.WriteString(strings.TrimSpace(content) + "\n"); err != nil {
		_ = file.Close()
		return composeError(ErrComposeConfigInvalid, "无法写入 Compose 预览文件")
	}
	if err := file.Close(); err != nil {
		return composeError(ErrComposeConfigInvalid, "无法保存 Compose 预览文件")
	}
	candidate := target
	candidate.ConfigFiles = []string{path}
	return action(candidate)
}

func (s *Service) composeArgs(target ComposeProjectTarget, actionArgs ...string) ([]string, error) {
	name, err := validateComposeProjectName(target.ProjectName)
	if err != nil {
		return nil, err
	}
	if len(target.ConfigFiles) == 0 || target.WorkingDir == "" || !filepath.IsAbs(target.WorkingDir) || strings.ContainsAny(target.WorkingDir, "\r\n") {
		return nil, composeError(ErrComposeConfigUnavailable, "Compose 项目配置路径不可用")
	}
	args := []string{"compose", "--ansi", "never", "--project-name", name, "--project-directory", target.WorkingDir}
	for _, path := range target.ConfigFiles {
		path = filepath.Clean(path)
		if path == "" || strings.ContainsAny(path, "\r\n") {
			return nil, composeError(ErrComposeConfigUnavailable, "Compose 配置路径无效")
		}
		if !filepath.IsAbs(path) {
			return nil, composeError(ErrComposeConfigUnavailable, "Compose 配置路径必须是绝对路径")
		}
		args = append(args, "--file", path)
	}
	return append(args, actionArgs...), nil
}

func (s *Service) composeRun(ctx context.Context, target ComposeProjectTarget, actionArgs ...string) (string, error) {
	args, err := s.composeArgs(target, actionArgs...)
	if err != nil {
		return "", err
	}
	out, err := s.runWithTimeout(ctx, 35*time.Minute, args...)
	if err == nil {
		return out, nil
	}
	if errors.Is(err, ErrDockerCommandTimeout) {
		return "", composeError(ErrComposeOperationTimeout, "Docker Compose 操作超时")
	}
	if errors.Is(err, ErrRuntimeUnavailable) {
		return "", composeError(ErrRuntimeUnavailable, "Docker 守护进程不可用")
	}
	return "", composeError(ErrComposeOperationFailed, "Docker Compose 操作失败")
}

func (s *Service) composeJSON(ctx context.Context, target ComposeProjectTarget, actionArgs ...string) ([]map[string]any, error) {
	out, err := s.composeRun(ctx, target, actionArgs...)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "null" {
		return []map[string]any{}, nil
	}
	var array []map[string]any
	if err := json.Unmarshal([]byte(trimmed), &array); err == nil {
		if array == nil {
			return []map[string]any{}, nil
		}
		return array, nil
	}
	items := make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return nil, composeError(ErrComposeOperationFailed, "Docker Compose 服务状态格式无效")
		}
		items = append(items, item)
	}
	if err := scanner.Err(); err != nil {
		return nil, composeError(ErrComposeOperationFailed, "Docker Compose 服务状态读取失败")
	}
	return items, nil
}

func (s *Service) ComposeConfig(ctx context.Context, name string) (string, ComposeProjectTarget, error) {
	target, err := s.ResolveComposeProject(ctx, name)
	if err != nil {
		return "", ComposeProjectTarget{}, err
	}
	readable, editable, reason := composeConfigState(target)
	if !readable {
		return "", target, composeError(ErrComposeConfigUnavailable, reason)
	}
	if !editable {
		return "", target, composeError(ErrComposeMultiFile, reason)
	}
	content, err := os.ReadFile(target.ConfigFiles[0])
	if err != nil {
		return "", target, composeError(ErrComposeConfigUnavailable, "Compose 配置文件读取失败")
	}
	return string(content), target, nil
}

// GetComposeConfigView returns a safe editor payload by default. The reveal
// path still uses the same file and permission checks, but the handler must
// authenticate the operator before requesting plaintext.
func (s *Service) GetComposeConfigView(ctx context.Context, name string, reveal bool) (ComposeConfigView, error) {
	content, target, err := s.ComposeConfig(ctx, name)
	if err != nil {
		return ComposeConfigView{}, err
	}

	view := ComposeConfigView{
		ProjectName:        target.ProjectName,
		ConfigFile:         target.ConfigFiles[0],
		Content:            content,
		ContentMode:        "plaintext",
		ConfigRevision:     hashContent(content),
		RedactionAvailable: true,
	}
	redacted, fields, redactionErr := redactComposeConfig(content)
	view.ContainsSensitive = len(fields) > 0 || redactionErr != nil
	view.SensitiveFields = fields
	if redactionErr != nil {
		view.RedactionAvailable = false
		view.RedactionReason = "当前 Compose 配置无法安全脱敏展示，请通过明文查看后编辑"
	}
	if reveal {
		return view, nil
	}

	view.ContentMode = "redacted"
	if redactionErr != nil {
		view.Content = "# 当前 Compose 配置无法安全脱敏展示，请点击“查看敏感配置并编辑”查看原文。\n"
		return view, nil
	}
	view.Content = redacted
	return view, nil
}

func redactComposeConfig(content string) (string, []ComposeSensitiveField, error) {
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(content), &document); err != nil {
		return "", nil, err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return "", nil, errors.New("compose yaml document is not a single document")
	}

	fields := make([]ComposeSensitiveField, 0)
	seen := make(map[string]struct{})
	redactComposeYAMLNode(document.Content[0], nil, "", false, &fields, seen)
	encoded, err := yaml.Marshal(&document)
	if err != nil {
		return "", nil, err
	}
	return string(encoded), fields, nil
}

func redactComposeYAMLNode(node *yaml.Node, path []string, parentKey string, skipKeyMatch bool, fields *[]ComposeSensitiveField, seen map[string]struct{}) bool {
	if node == nil {
		return false
	}
	clearComposeYAMLComments(node)
	changed := false
	switch node.Kind {
	case yaml.DocumentNode:
		for _, child := range node.Content {
			changed = redactComposeYAMLNode(child, path, parentKey, false, fields, seen) || changed
		}
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			keyNode, valueNode := node.Content[index], node.Content[index+1]
			key := strings.TrimSpace(keyNode.Value)
			childPath := append(append([]string(nil), path...), key)
			kind := ""
			if !skipKeyMatch {
				kind = composeSensitiveKeyKind(key)
			}
			if kind != "" {
				addComposeSensitiveField(fields, seen, composeYAMLPath(childPath), kind)
				changed = redactComposeSensitiveNode(valueNode, childPath, fields, seen) || changed
				continue
			}
			childSkipKeyMatch := strings.EqualFold(key, "secrets") || strings.EqualFold(key, "configs")
			changed = redactComposeYAMLNode(valueNode, childPath, key, childSkipKeyMatch, fields, seen) || changed
		}
	case yaml.SequenceNode:
		for index, child := range node.Content {
			itemPath := append(append([]string(nil), path...), "["+strconv.Itoa(index)+"]")
			if (strings.EqualFold(parentKey, "environment") || strings.EqualFold(parentKey, "labels")) && child.Kind == yaml.ScalarNode {
				key, separator, assignedValue, ok := splitComposeAssignment(child.Value)
				if ok {
					if kind := composeSensitiveKeyKind(key); kind != "" {
						fieldPath := append(append([]string(nil), path...), "["+strconv.Itoa(index)+"]"+"."+key)
						addComposeSensitiveField(fields, seen, composeYAMLPath(fieldPath), kind)
						if !composeExternalSecretReference(assignedValue) {
							child.Value = key + separator + composeRedactedSecretMarker
							child.Tag = "!!str"
							changed = true
						}
						continue
					}
				}
			}
			changed = redactComposeYAMLNode(child, itemPath, parentKey, false, fields, seen) || changed
		}
	case yaml.ScalarNode:
		if redacted, ok := redactComposeEmbeddedSecret(node.Value); ok {
			node.Value = redacted
			node.Tag = "!!str"
			addComposeSensitiveField(fields, seen, composeYAMLPath(path), "embedded_credential")
			changed = true
		}
	case yaml.AliasNode:
		// Anchors are visited at their definition. Following aliases here can
		// recurse forever on valid recursive YAML documents.
	}
	return changed
}

func redactComposeSensitiveNode(node *yaml.Node, path []string, fields *[]ComposeSensitiveField, seen map[string]struct{}) bool {
	if node == nil {
		return false
	}
	clearComposeYAMLComments(node)
	switch node.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(node.Value) == "" || composeExternalSecretReference(node.Value) {
			return false
		}
		node.Value = composeRedactedSecretMarker
		node.Tag = "!!str"
		return true
	case yaml.SequenceNode:
		changed := false
		for _, child := range node.Content {
			changed = redactComposeSensitiveNode(child, path, fields, seen) || changed
		}
		return changed
	case yaml.MappingNode:
		changed := false
		for index := 0; index+1 < len(node.Content); index += 2 {
			clearComposeYAMLComments(node.Content[index])
			changed = redactComposeSensitiveNode(node.Content[index+1], path, fields, seen) || changed
		}
		return changed
	case yaml.AliasNode:
		// Redact the anchor definition as well as the alias. The visited set
		// prevents recursive aliases from causing an infinite traversal.
		redactComposeAliasTarget(node.Alias, make(map[*yaml.Node]struct{}))
		node.Kind = yaml.ScalarNode
		node.Tag = "!!str"
		node.Value = composeRedactedSecretMarker
		node.Alias = nil
		return true
	default:
		return false
	}
}

func redactComposeAliasTarget(node *yaml.Node, visited map[*yaml.Node]struct{}) {
	if node == nil {
		return
	}
	if _, ok := visited[node]; ok {
		return
	}
	visited[node] = struct{}{}
	clearComposeYAMLComments(node)
	switch node.Kind {
	case yaml.ScalarNode:
		if !composeExternalSecretReference(node.Value) {
			node.Value = composeRedactedSecretMarker
			node.Tag = "!!str"
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			redactComposeAliasTarget(child, visited)
		}
	case yaml.MappingNode:
		for index := 0; index+1 < len(node.Content); index += 2 {
			clearComposeYAMLComments(node.Content[index])
			redactComposeAliasTarget(node.Content[index+1], visited)
		}
	case yaml.AliasNode:
		redactComposeAliasTarget(node.Alias, visited)
	}
}

func redactComposeEmbeddedSecret(value string) (string, bool) {
	if strings.TrimSpace(value) == "" || composeExternalSecretReference(value) {
		return value, false
	}
	if strings.Contains(value, "-----BEGIN") {
		return composeRedactedSecretMarker, true
	}
	redacted := composeSecretURLPattern.ReplaceAllString(value, `${1}`+composeRedactedSecretMarker+`${4}`)
	redacted = containerSensitiveLogAssignmentPattern.ReplaceAllString(redacted, `${1}${2}`+composeRedactedSecretMarker)
	redacted = composeSecretFlagPattern.ReplaceAllStringFunc(redacted, func(match string) string {
		parts := composeSecretFlagPattern.FindStringSubmatch(match)
		if len(parts) != 3 || composeExternalSecretReference(strings.Trim(parts[2], "\"'")) {
			return match
		}
		return parts[1] + composeRedactedSecretMarker
	})
	return redacted, redacted != value
}

func composeSensitiveKeyKind(key string) string {
	key = strings.TrimSpace(key)
	if key == "" || !composeSensitiveKeyPattern.MatchString(key) {
		return ""
	}
	lower := strings.ToLower(key)
	switch {
	case strings.Contains(lower, "password"), strings.Contains(lower, "passwd"), strings.Contains(lower, "pwd"), strings.Contains(lower, "pass"):
		return "password"
	case strings.Contains(lower, "token"), strings.Contains(lower, "cookie"), strings.Contains(lower, "session"):
		return "token"
	case strings.Contains(lower, "key"), strings.Contains(lower, "private"):
		return "key"
	case strings.Contains(lower, "credential"), strings.Contains(lower, "auth"), strings.Contains(lower, "dsn"), strings.Contains(lower, "url"), strings.Contains(lower, "connection"):
		return "credential"
	default:
		return "secret"
	}
}

func splitComposeAssignment(value string) (string, string, string, bool) {
	index := strings.IndexByte(value, '=')
	if index <= 0 {
		return "", "", "", false
	}
	key := strings.TrimSpace(value[:index])
	if key == "" {
		return "", "", "", false
	}
	return key, "=", value[index+1:], true
}

func composeExternalSecretReference(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return true
	}
	if !strings.HasPrefix(value, "$") || len(value) < 2 {
		return false
	}
	for index, char := range value[1:] {
		if (index == 0 && !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || char == '_')) ||
			(index > 0 && !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_')) {
			return false
		}
	}
	return true
}

func composeYAMLPath(parts []string) string {
	result := ""
	for _, part := range parts {
		if strings.HasPrefix(part, "[") {
			result += part
		} else if result == "" {
			result = part
		} else {
			result += "." + part
		}
	}
	return result
}

func addComposeSensitiveField(fields *[]ComposeSensitiveField, seen map[string]struct{}, path, kind string) {
	if path == "" {
		path = "<document>"
	}
	key := path + "\x00" + kind
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*fields = append(*fields, ComposeSensitiveField{Path: path, Kind: kind})
}

func clearComposeYAMLComments(node *yaml.Node) {
	if node == nil {
		return
	}
	node.HeadComment, node.LineComment, node.FootComment = "", "", ""
}

func (s *Service) restoreComposeRedactedContent(target ComposeProjectTarget, content string) (string, error) {
	if !strings.Contains(content, composeRedactedSecretMarker) {
		return content, nil
	}
	if len(target.ConfigFiles) != 1 {
		return "", composeError(ErrComposeMultiFile, "当前项目使用多个 Compose 配置文件，无法保留脱敏字段")
	}
	original, err := os.ReadFile(target.ConfigFiles[0])
	if err != nil {
		return "", composeError(ErrComposeConfigUnavailable, "无法读取原 Compose 配置以保留敏感字段")
	}
	return restoreComposeRedactedValues(string(original), content)
}

func restoreComposeRedactedValues(original, candidate string) (string, error) {
	var originalDocument, candidateDocument yaml.Node
	if err := yaml.Unmarshal([]byte(original), &originalDocument); err != nil {
		return "", composeError(ErrComposeConfigInvalid, "原 Compose 配置无法解析，无法安全保留敏感字段")
	}
	if err := yaml.Unmarshal([]byte(candidate), &candidateDocument); err != nil {
		return "", composeError(ErrComposeConfigInvalid, "脱敏 Compose 配置无法解析，请重新打开编辑器或查看明文后再保存")
	}
	if originalDocument.Kind != yaml.DocumentNode || len(originalDocument.Content) != 1 || candidateDocument.Kind != yaml.DocumentNode || len(candidateDocument.Content) != 1 {
		return "", composeError(ErrComposeConfigInvalid, "Compose 配置文档结构无效，无法安全保留敏感字段")
	}
	mergeComposeRedactedValues(originalDocument.Content[0], candidateDocument.Content[0], "", false)
	encoded, err := yaml.Marshal(&candidateDocument)
	if err != nil {
		return "", composeError(ErrComposeConfigInvalid, "脱敏 Compose 配置无法保存")
	}
	if strings.Contains(string(encoded), composeRedactedSecretMarker) {
		return "", composeError(ErrComposeConfigInvalid, "Compose 配置包含未解析的敏感字段，请查看明文后再保存")
	}
	return string(encoded), nil
}

func mergeComposeRedactedValues(original, candidate *yaml.Node, parentKey string, skipKeyMatch bool) {
	if original == nil || candidate == nil || original.Kind != candidate.Kind {
		return
	}
	switch candidate.Kind {
	case yaml.DocumentNode:
		for index := range candidate.Content {
			if index < len(original.Content) {
				mergeComposeRedactedValues(original.Content[index], candidate.Content[index], parentKey, false)
			}
		}
	case yaml.MappingNode:
		originalValues := composeYAMLMappingValues(original)
		for index := 0; index+1 < len(candidate.Content); index += 2 {
			keyNode, valueNode := candidate.Content[index], candidate.Content[index+1]
			key := strings.TrimSpace(keyNode.Value)
			kind := ""
			if !skipKeyMatch {
				kind = composeSensitiveKeyKind(key)
			}
			originalValue := originalValues[key]
			if kind != "" && originalValue != nil && composeYAMLNodeContainsMarker(valueNode) {
				*candidate.Content[index+1] = *originalValue
				continue
			}
			childSkipKeyMatch := strings.EqualFold(key, "secrets") || strings.EqualFold(key, "configs")
			mergeComposeRedactedValues(originalValue, valueNode, key, childSkipKeyMatch)
		}
	case yaml.SequenceNode:
		for index, valueNode := range candidate.Content {
			if valueNode.Kind == yaml.ScalarNode && (strings.EqualFold(parentKey, "environment") || strings.EqualFold(parentKey, "labels")) {
				key, _, candidateValue, ok := splitComposeAssignment(valueNode.Value)
				if ok && strings.Contains(candidateValue, composeRedactedSecretMarker) {
					if originalValue := composeYAMLAssignmentValue(original, key); originalValue != nil {
						*candidate.Content[index] = *originalValue
						continue
					}
				}
			}
			if index < len(original.Content) {
				mergeComposeRedactedValues(original.Content[index], valueNode, parentKey, false)
			}
		}
	case yaml.ScalarNode:
		if !strings.Contains(candidate.Value, composeRedactedSecretMarker) || original.Kind != yaml.ScalarNode {
			return
		}
		redactedOriginal, changed := redactComposeEmbeddedSecret(original.Value)
		if changed && redactedOriginal == candidate.Value {
			*candidate = *original
		}
	}
}

func composeYAMLMappingValues(node *yaml.Node) map[string]*yaml.Node {
	values := make(map[string]*yaml.Node)
	if node == nil || node.Kind != yaml.MappingNode {
		return values
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		values[strings.TrimSpace(node.Content[index].Value)] = node.Content[index+1]
	}
	return values
}

func composeYAMLAssignmentValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	for _, item := range node.Content {
		if item.Kind != yaml.ScalarNode {
			continue
		}
		itemKey, _, _, ok := splitComposeAssignment(item.Value)
		if ok && itemKey == key {
			return item
		}
	}
	return nil
}

func composeYAMLNodeContainsMarker(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.ScalarNode {
		return strings.Contains(node.Value, composeRedactedSecretMarker)
	}
	for _, child := range node.Content {
		if composeYAMLNodeContainsMarker(child) {
			return true
		}
	}
	return false
}

func (s *Service) StageComposeContent(target ComposeProjectTarget, content string) (string, string, error) {
	if _, err := validateComposeContent(content, target.WorkingDir); err != nil {
		return "", "", err
	}
	if err := ensureComposeWorkingDir(target); err != nil {
		return "", "", err
	}
	file, err := os.CreateTemp(target.WorkingDir, ".oneinstack-compose-task-*.yml")
	if err != nil {
		return "", "", composeError(ErrComposeConfigInvalid, "无法创建 Compose 任务暂存文件")
	}
	path := file.Name()
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", "", composeError(ErrComposeConfigInvalid, "无法设置 Compose 任务暂存文件权限")
	}
	if _, err := file.WriteString(strings.TrimSpace(content) + "\n"); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", "", composeError(ErrComposeConfigInvalid, "无法写入 Compose 任务暂存文件")
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", "", composeError(ErrComposeConfigInvalid, "无法保存 Compose 任务暂存文件")
	}
	return path, hashContent(content), nil
}

func (s *Service) ComposeTemplateContent(ctx context.Context, id uint) (string, error) {
	if id == 0 {
		return "", composeError(ErrComposeTemplateNotFound, "Compose 模板不存在")
	}
	document, err := s.GetTemplate(ctx, id)
	if err != nil {
		return "", composeError(ErrComposeTemplateNotFound, "Compose 模板不存在")
	}
	if _, err := validateComposeContent(document.Content, ""); err != nil {
		return "", err
	}
	return document.Content, nil
}

func cleanupComposeTask(request *ComposeTaskRequest) {
	if request == nil || request.ContentPath == "" {
		return
	}
	path := filepath.Clean(request.ContentPath)
	if !strings.HasPrefix(filepath.Base(path), ".oneinstack-compose-task-") {
		return
	}
	_ = os.Remove(path)
}

func RemoveComposeTaskContent(path string) {
	cleanupComposeTask(&ComposeTaskRequest{ContentPath: path})
}

func (s *Service) RunComposeTask(ctx context.Context, operation string, request ComposeTaskRequest, emit func(string)) error {
	if err := validateComposeOperation(operation); err != nil {
		return err
	}
	if request.ProjectName == "" {
		return composeError(ErrComposeConfigInvalid, "Compose 项目名称不能为空")
	}
	target := request.Target
	if operation == models.ContainerTaskOperationComposeCreate {
		target = managedComposeTarget(request.ProjectName)
	} else {
		resolved, err := s.ResolveComposeProject(ctx, request.ProjectName)
		if err != nil {
			return err
		}
		target = resolved
	}
	if operation == models.ContainerTaskOperationComposeCreate || operation == models.ContainerTaskOperationComposeEdit {
		if request.ContentPath == "" {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置内容缺失")
		}
		content, err := os.ReadFile(request.ContentPath)
		if err != nil {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置暂存文件不可读取")
		}
		if request.ContentHash != "" && request.ContentHash != hashContent(string(content)) {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置内容校验失败")
		}
		if err := s.withComposeCandidate(target, string(content), func(candidate ComposeProjectTarget) error {
			_, err := s.composeRun(ctx, candidate, "config", "--quiet")
			return err
		}); err != nil {
			return err
		}
		if operation == models.ContainerTaskOperationComposeEdit {
			return s.applyComposeConfig(target, string(content))
		}
		if err := ensureComposeWorkingDir(target); err != nil {
			return err
		}
		configPath := filepath.Join(target.WorkingDir, "compose.yml")
		if _, err := os.Stat(configPath); err == nil {
			return composeError(ErrComposeProjectConflict, "Compose 项目配置已存在")
		}
		if err := os.Rename(request.ContentPath, configPath); err != nil {
			return composeError(ErrComposeConfigInvalid, "无法保存 Compose 项目配置")
		}
		target.ConfigFiles = []string{configPath}
	}

	var args []string
	switch operation {
	case models.ContainerTaskOperationComposeCreate:
		args = []string{"up", "-d", "--build"}
	case models.ContainerTaskOperationComposeStart:
		args = []string{"up", "-d"}
	case models.ContainerTaskOperationComposeStop:
		args = []string{"stop"}
	case models.ContainerTaskOperationComposeRestart:
		args = []string{"restart"}
	case models.ContainerTaskOperationComposeUpdate:
		if err := s.composeStream(ctx, target, emit, "pull"); err != nil {
			return err
		}
		args = []string{"up", "-d", "--build"}
	case models.ContainerTaskOperationComposeDelete:
		args = []string{"down", "--remove-orphans"}
		if request.RemoveVolumes {
			args = append(args, "--volumes")
		}
	case models.ContainerTaskOperationComposeEdit:
		return nil
	}
	if err := s.composeStream(ctx, target, emit, args...); err != nil {
		if operation == models.ContainerTaskOperationComposeCreate && target.Managed {
			_, _ = s.composeRun(ctx, target, "down", "--remove-orphans")
		}
		return err
	}
	if err := s.verifyComposeTask(ctx, operation, target); err != nil {
		return err
	}
	if operation == models.ContainerTaskOperationComposeDelete && target.Managed {
		configPath := filepath.Join(target.WorkingDir, "compose.yml")
		_ = os.Remove(configPath)
	}
	return nil
}

func (s *Service) ValidateComposeTaskPreview(ctx context.Context, operation string, request ComposeTaskRequest) error {
	if operation != models.ContainerTaskOperationComposeCreate &&
		operation != models.ContainerTaskOperationComposeEdit &&
		operation != models.ContainerTaskOperationComposeUpdate &&
		operation != models.ContainerTaskOperationComposeDelete {
		return nil
	}
	if strings.TrimSpace(request.PreviewHash) == "" {
		return composeError(ErrComposePreviewStale, "Compose 预览已失效，请重新预览")
	}
	action := strings.TrimPrefix(operation, "compose.")
	content := ""
	if action == "create" || action == "edit" {
		if request.ContentPath == "" {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置内容缺失")
		}
		data, err := os.ReadFile(request.ContentPath)
		if err != nil {
			return composeError(ErrComposeConfigInvalid, "Compose 任务配置暂存文件不可读取")
		}
		content = string(data)
	}
	preview, err := s.ComposePreview(ctx, action, request.ProjectName, content, request.RemoveVolumes)
	if err != nil {
		return err
	}
	if preview.PreviewFingerprint != request.PreviewHash {
		return composeError(ErrComposePreviewStale, "Compose 预览已失效，请重新预览")
	}
	return nil
}

func (s *Service) applyComposeConfig(target ComposeProjectTarget, content string) error {
	if len(target.ConfigFiles) != 1 {
		return composeError(ErrComposeMultiFile, "当前项目使用多个 Compose 配置文件，暂不支持单 YAML 编辑")
	}
	path := filepath.Clean(target.ConfigFiles[0])
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return composeError(ErrComposeConfigUnavailable, "Compose 配置文件不可安全覆盖")
	}
	if err := os.MkdirAll(composeBackupRoot(), 0700); err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法准备 Compose 配置备份目录")
	}
	backupDir := filepath.Join(composeBackupRoot(), hashContent(path)[:24])
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法准备 Compose 配置备份目录")
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法读取原 Compose 配置")
	}
	backup := filepath.Join(backupDir, time.Now().UTC().Format("20060102T150405.000000000Z")+".yml")
	if err := os.WriteFile(backup, old, 0600); err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法备份原 Compose 配置")
	}
	entries, _ := os.ReadDir(backupDir)
	if len(entries) > composeBackupLimit {
		files := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() {
				files = append(files, entry.Name())
			}
		}
		sort.Strings(files)
		for len(files) > composeBackupLimit {
			_ = os.Remove(filepath.Join(backupDir, files[0]))
			files = files[1:]
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".oneinstack-compose-atomic-*.yml")
	if err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法创建 Compose 原子替换文件")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		_ = temporary.Close()
		return composeError(ErrComposeConfigUnavailable, "无法设置 Compose 配置权限")
	}
	if _, err := temporary.WriteString(strings.TrimSpace(content) + "\n"); err != nil {
		_ = temporary.Close()
		return composeError(ErrComposeConfigUnavailable, "无法写入 Compose 原子替换文件")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return composeError(ErrComposeConfigUnavailable, "无法持久化 Compose 配置")
	}
	if err := temporary.Close(); err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法关闭 Compose 原子替换文件")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return composeError(ErrComposeConfigUnavailable, "无法原子替换 Compose 配置")
	}
	return nil
}

func (s *Service) verifyComposeTask(ctx context.Context, operation string, target ComposeProjectTarget) error {
	services, err := s.composeJSON(ctx, target, "ps", "--all", "--format", "json")
	if err != nil {
		return err
	}
	if operation != models.ContainerTaskOperationComposeDelete && operation != models.ContainerTaskOperationComposeStop && len(services) == 0 {
		return composeError(ErrComposeOperationFailed, "Compose 项目未发现服务资源")
	}
	if operation == models.ContainerTaskOperationComposeDelete {
		if len(services) > 0 {
			return composeError(ErrComposeOperationFailed, "Compose 项目资源未完全删除")
		}
		return nil
	}
	if operation == models.ContainerTaskOperationComposeStop {
		for _, service := range services {
			status := strings.ToLower(parseComposeStringField(service, "State"))
			if status == "running" {
				return composeError(ErrComposeOperationFailed, "Compose 项目仍有服务处于运行状态")
			}
		}
	}
	if operation == models.ContainerTaskOperationComposeCreate ||
		operation == models.ContainerTaskOperationComposeStart ||
		operation == models.ContainerTaskOperationComposeRestart ||
		operation == models.ContainerTaskOperationComposeUpdate {
		for _, service := range services {
			status := strings.ToLower(parseComposeStringField(service, "State"))
			if status != "running" {
				return composeError(ErrComposeOperationFailed, "Compose 项目启动后仍有服务未处于运行状态")
			}
		}
	}
	return nil
}

func (s *Service) composeStream(ctx context.Context, target ComposeProjectTarget, emit func(string), actionArgs ...string) error {
	args, err := s.composeArgs(target, actionArgs...)
	if err != nil {
		return err
	}
	streamEmit := func(line string) {
		if emit != nil {
			emit(redactContainerLogLine(line))
		}
	}
	if err := s.runStreaming(ctx, 35*time.Minute, args, streamEmit); err != nil {
		if errors.Is(err, ErrDockerCommandTimeout) {
			return composeError(ErrComposeOperationTimeout, "Docker Compose 操作超时")
		}
		if errors.Is(err, ErrRuntimeUnavailable) {
			return composeError(ErrRuntimeUnavailable, "Docker 守护进程不可用")
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "compose") && strings.Contains(lower, "not a docker command") {
			return composeError(ErrComposeUnavailable, "Docker Compose 插件不可用")
		}
		return composeError(ErrComposeOperationFailed, "Docker Compose 操作失败，请查看任务日志中的脱敏输出")
	}
	return nil
}

func composeLogArgs(target ComposeProjectTarget, options ComposeLogOptions, follow bool) ([]string, error) {
	if err := ValidateLogOptions(LogOptions{Tail: options.Tail, Since: options.Since, Until: options.Until, Timestamps: options.Timestamps}); err != nil {
		return nil, err
	}
	args, err := (&Service{binary: "docker"}).composeArgs(target, "logs", "--tail", strconv.Itoa(options.Tail))
	if err != nil {
		return nil, err
	}
	if options.Timestamps {
		args = append(args, "--timestamps")
	}
	if options.Since != "" {
		args = append(args, "--since", options.Since)
	}
	if options.Until != "" {
		args = append(args, "--until", options.Until)
	}
	if follow {
		args = append(args, "--follow")
	}
	if options.Service != "" {
		if err := validateComposeServiceName(options.Service); err != nil {
			return nil, err
		}
		args = append(args, options.Service)
	}
	return args, nil
}

func (s *Service) ComposeLogs(ctx context.Context, target ComposeProjectTarget, options ComposeLogOptions) (string, error) {
	args, err := composeLogArgs(target, options, false)
	if err != nil {
		return "", err
	}
	if err := s.ensureComposeAvailable(ctx); err != nil {
		return "", err
	}
	var output bytes.Buffer
	if err := s.runComposeLogs(ctx, args, &output); err != nil {
		return "", err
	}
	return output.String(), nil
}

func (s *Service) FollowComposeLogs(ctx context.Context, target ComposeProjectTarget, options ComposeLogOptions, output io.Writer) error {
	args, err := composeLogArgs(target, options, true)
	if err != nil {
		return err
	}
	if err := s.ensureComposeAvailable(ctx); err != nil {
		return err
	}
	return s.runComposeLogs(ctx, args, output)
}

func (s *Service) runComposeLogs(ctx context.Context, args []string, output io.Writer) error {
	if _, err := exec.LookPath(s.binary); err != nil {
		return composeError(ErrRuntimeUnavailable, "Docker 运行时不可用")
	}
	var commandCtx context.Context
	var cancel context.CancelFunc
	if containsComposeArgument(args, "--follow") {
		commandCtx, cancel = context.WithCancel(ctx)
	} else {
		commandCtx, cancel = context.WithTimeout(ctx, 60*time.Second)
	}
	defer cancel()
	command := exec.CommandContext(commandCtx, s.binary, args...)
	redacting := newContainerLogRedactingWriter(output)
	command.Stdout = redacting
	command.Stderr = redacting
	if err := command.Run(); err != nil {
		_ = redacting.Flush()
		if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
			return composeError(ErrComposeOperationTimeout, "读取 Compose 日志超时")
		}
		if errors.Is(commandCtx.Err(), context.Canceled) {
			return context.Canceled
		}
		return composeError(ErrComposeOperationFailed, "读取 Compose 日志失败")
	}
	return redacting.Flush()
}

func containsComposeArgument(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}
