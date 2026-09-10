package software

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/script"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"

	"gorm.io/gorm"
)

// InstallParameterError identifies a safe, client-actionable installation
// parameter error. It is returned before a software task is created.
type InstallParameterError struct {
	Field   string
	Message string
}

func (e *InstallParameterError) Error() string {
	if e == nil {
		return "software installation parameters are invalid"
	}
	if strings.TrimSpace(e.Field) == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

// UserMessage keeps the historical user-facing conversion used by component
// configuration handlers. Installation handlers should use
// InstallationMessage, which covers the complete install validation surface.
func (e *InstallParameterError) UserMessage() string {
	if e == nil {
		return ""
	}
	return installPortUserMessage(strings.TrimSpace(e.Message))
}

// InstallationMessage returns a safe, actionable Chinese message for an
// installation parameter error. The HTTP response layer translates this
// message according to Accept-Language, while the internal validation text
// remains stable for logs and existing callers.
func (e *InstallParameterError) InstallationMessage() string {
	if e == nil {
		return ""
	}

	field := strings.TrimSpace(e.Field)
	message := strings.TrimSpace(e.Message)
	if portMessage := installPortUserMessage(message); portMessage != "" {
		return portMessage
	}

	switch message {
	case "installation parameters are required":
		return "安装请求不能为空，请提供安装参数后重试"
	case "must match version when both fields are provided":
		return "version 与 software-version 参数不一致，请保持两者一致后重试"
	case "must match the port parameter when both fields are provided":
		return "port 与端口参数不一致，请保持两者一致后重试"
	case "is required":
		switch field {
		case "key":
			return "未填写软件标识 key，请提供要安装的软件后重试"
		case "version":
			return "未填写软件版本 version，请提供要安装的版本后重试"
		case "port":
			return "未填写监听端口 port，请提供端口后重试"
		default:
			if field != "" && field != "parameters" {
				return fmt.Sprintf("安装参数 %s 未填写，请补充该参数后重试", field)
			}
			return "安装参数未填写，请补充必填参数后重试"
		}
	case "必须是规范化的绝对路径":
		if field != "" {
			return fmt.Sprintf("安装参数 %s 必须是规范化的绝对路径（以 / 开头且不包含 ..），请修正后重试", field)
		}
		return "安装参数必须是规范化的绝对路径，请修正后重试"
	case "目录范围过宽":
		if field != "" {
			return fmt.Sprintf("安装参数 %s 不能使用过于宽泛的系统目录，请指定更具体的目录后重试", field)
		}
		return "安装参数不能使用过于宽泛的系统目录，请指定更具体的目录后重试"
	case "PHP 版本必须是 8.x.y 格式，并且属于 Center 已发布的版本线":
		return message
	case "MySQL 运行账户必须以小写字母或下划线开头，仅允许小写字母、数字、下划线和连字符，长度为 1-32 个字符",
		"MySQL 登录用户必须以小写字母或下划线开头，仅允许小写字母、数字、下划线和连字符，长度为 1-32 个字符",
		"MySQL 密码必须为 12-128 个字符，仅允许字母、数字及 _ @ % + = : , . ! # ? -":
		return message
	}
	if strings.HasPrefix(message, "PHP 版本线 ") && strings.HasSuffix(message, " 尚未由 Center 发布") {
		return message
	}

	if strings.HasPrefix(message, "component parameter ") {
		const prefix = "component parameter "
		parts := strings.SplitN(strings.TrimPrefix(message, prefix), " ", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			parameter := parts[0]
			switch parts[1] {
			case "is required":
				return fmt.Sprintf("安装参数 %s 未填写，请补充该参数后重试", parameter)
			case "must be an integer":
				return fmt.Sprintf("安装参数 %s 必须是整数，请修正后重试", parameter)
			case "must be a valid port":
				return fmt.Sprintf("安装参数 %s 必须是 1 到 65535 之间的有效端口，请修正后重试", parameter)
			case "must be true or false":
				return fmt.Sprintf("安装参数 %s 必须是 true 或 false，请修正后重试", parameter)
			case "must be a normalized absolute path":
				return fmt.Sprintf("安装参数 %s 必须是规范化的绝对路径（以 / 开头且不包含 ..），请修正后重试", parameter)
			case "contains invalid data":
				return fmt.Sprintf("安装参数 %s 包含不允许的内容，请修正后重试", parameter)
			case "is reserved":
				return fmt.Sprintf("安装参数 %s 使用了保留名称，请刷新组件安装包后重试", parameter)
			default:
				if strings.HasPrefix(parts[1], "has unsupported type ") {
					return fmt.Sprintf("安装参数 %s 使用了不支持的类型，请刷新组件安装参数定义后重试", parameter)
				}
			}
		}
	}

	if field == "parameters" {
		return "组件安装参数无效，请检查字段类型、格式和取值范围后重试"
	}
	if field != "" {
		return fmt.Sprintf("安装参数 %s 无效，请检查字段类型、格式和取值范围后重试", field)
	}
	return "安装参数无效，请检查字段类型、格式和取值范围后重试"
}

func installPortUserMessage(message string) string {
	const inUseSuffix = " is already in use"
	const availabilitySuffix = " availability could not be confirmed"
	for _, item := range []struct {
		suffix  string
		message string
	}{
		{
			suffix:  inUseSuffix,
			message: "监听端口 %s 已被占用，请更换未占用的端口后重试",
		},
		{
			suffix:  availabilitySuffix,
			message: "无法确认监听端口 %s 是否可用，请检查端口状态后重试",
		},
	} {
		if !strings.HasPrefix(message, "port ") || !strings.HasSuffix(message, item.suffix) {
			continue
		}
		port := strings.TrimSuffix(strings.TrimPrefix(message, "port "), item.suffix)
		if _, err := strconv.Atoi(port); err != nil {
			return ""
		}
		return fmt.Sprintf(item.message, port)
	}
	return ""
}

// EffectiveInstallParameter describes the value that the installation
// pipeline will use after applying common and signed-manifest defaults.
// Sensitive values intentionally do not leave the backend.
type EffectiveInstallParameter struct {
	Key       string
	Value     string
	Sensitive bool
	Source    string
}

var (
	managedMySQLUsernamePattern         = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	managedMySQLDatabaseUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	managedMySQLPasswordPattern         = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,.!#?-]{12,128}$`)
	phpExactVersionPattern              = regexp.MustCompile(`^8\.[0-9]+\.[0-9]+$`)
	phpVersionLinePattern               = regexp.MustCompile(`^8\.[0-9]+\.x$`)
)

// resolveInstallParams resolves the same package and parameter set used by
// the installer, validates it without executing an action, and returns the
// populated script metadata so preview callers can report effective values.
func (installer *Installer) resolveInstallParams(ctx context.Context, params *input.InstallParams) (*script.ScriptInfo, error) {
	if params == nil {
		return nil, &InstallParameterError{Field: "install", Message: "installation parameters are required"}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	flatVersion := strings.TrimSpace(params.Version)
	parameterVersion := installParameterValue(params.Parameters, "software-version", "version")
	flatPort := strings.TrimSpace(params.Port)
	parameterPort := installParameterValue(
		params.Parameters,
		"port",
		"nginx-port",
		"nginxPort",
		"mysql-port",
		"mysqlPort",
		"redis-port",
		"redisPort",
	)

	NormalizeInstallParams(params)
	if flatVersion != "" && parameterVersion != "" && flatVersion != parameterVersion {
		return nil, &InstallParameterError{
			Field:   "software-version",
			Message: "must match version when both fields are provided",
		}
	}
	if flatPort != "" && parameterPort != "" && flatPort != parameterPort {
		return nil, &InstallParameterError{
			Field:   "port",
			Message: "must match the port parameter when both fields are provided",
		}
	}
	if strings.TrimSpace(params.Key) == "" {
		return nil, &InstallParameterError{Field: "key", Message: "is required"}
	}
	if err := resolveFirewalldInstallParams(ctx, params); err != nil {
		return nil, err
	}
	if params.Version == "" {
		return nil, &InstallParameterError{Field: "version", Message: "is required"}
	}
	if err := ValidateManagedMySQLVersion(app.DB(), params.Key, params.Version); err != nil {
		return nil, err
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "php") {
		resolvedVersion, resolveErr := ResolvePHPVersionLine(app.DB(), params.Version)
		if resolveErr != nil {
			return nil, resolveErr
		}
		params.Version = resolvedVersion
		if err := resolvePHPInstallVersion(params); err != nil {
			return nil, err
		}
	}
	if params.Version == "" {
		return nil, &InstallParameterError{Field: "version", Message: "is required"}
	}
	if err := ValidateManagedMySQLInstallParams(params); err != nil {
		return nil, err
	}
	scriptInfo, err := installer.getInstallScript(ctx, params, "install")
	if err != nil {
		return nil, err
	}
	installer.setScriptParams(scriptInfo, params)
	if err := validateResolvedInstallPort(ctx, params, scriptInfo); err != nil {
		return nil, err
	}
	if err := script.ValidateParameters(scriptInfo); err != nil {
		return nil, &InstallParameterError{Field: "parameters", Message: err.Error()}
	}
	return scriptInfo, nil
}

// ValidateManagedMySQLVersion keeps the MySQL installation contract aligned
// with the Panel application list. The version line remains available to the
// signed component manifest, but it is never accepted as a user-facing
// installation version.
func ValidateManagedMySQLVersion(db *gorm.DB, key, version string) error {
	if !isManagedMySQLInstallKey(key) {
		return nil
	}
	version = strings.TrimSpace(version)
	if version != managedMySQLPublishedVersion {
		return &InstallParameterError{
			Field:   "version",
			Message: fmt.Sprintf("MySQL 版本必须选择 Panel 返回的精确可安装版本 %s", managedMySQLPublishedVersion),
		}
	}
	if db == nil {
		return nil
	}
	var row models.Software
	result := db.Where(
		"(`key` = ? OR component = ?) AND version = ?",
		"db", "mysql", version,
	).Order("catalog_managed DESC, catalog_visible DESC, id DESC").First(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return &InstallParameterError{
			Field:   "version",
			Message: fmt.Sprintf("MySQL 版本 %s 尚未由 Center 发布，请刷新软件目录后重试", version),
		}
	}
	if result.Error != nil {
		return fmt.Errorf("read MySQL catalog entry: %w", result.Error)
	}
	if row.CatalogManaged && (!row.CatalogVisible || !row.Installable) {
		return &InstallParameterError{
			Field:   "version",
			Message: fmt.Sprintf("MySQL 版本 %s 当前不可安装，请刷新软件目录后重试", version),
		}
	}
	if row.CatalogManaged && strings.TrimSpace(row.LatestPackageVersion) == "" {
		return &InstallParameterError{
			Field:   "version",
			Message: fmt.Sprintf("当前主机没有可用的 MySQL %s 安装制品，请刷新软件目录后重试", version),
		}
	}
	return nil
}

// resolvePHPInstallVersion validates the exact PHP patch shape after a
// catalog version line has been resolved. The published Center component
// version line is the source of truth for whether this patch is allowed;
// package resolution performs that line check before the task is queued.
func resolvePHPInstallVersion(params *input.InstallParams) error {
	if params == nil || !strings.EqualFold(strings.TrimSpace(params.Key), "php") {
		return nil
	}
	requested := strings.TrimSpace(params.Version)
	if !phpExactVersionPattern.MatchString(requested) {
		return &InstallParameterError{Field: "version", Message: "PHP 版本必须是 8.x.y 格式，并且属于 Center 已发布的版本线"}
	}
	return nil
}

func phpVersionLineForExact(version string) string {
	version = strings.TrimSpace(version)
	if !phpExactVersionPattern.MatchString(version) {
		return ""
	}
	parts := strings.Split(version, ".")
	return strings.Join(parts[:2], ".")
}

// ResolvePHPVersionLine resolves a PHP version line using the synced Center
// catalog, with the signed component's current release policy as the offline
// fallback. It is called before durable task creation so task fields never
// retain an unresolved version line.
func ResolvePHPVersionLine(db *gorm.DB, version string) (string, error) {
	version = strings.TrimSpace(version)
	if !phpVersionLinePattern.MatchString(version) {
		return version, nil
	}
	if db != nil {
		var row models.Software
		result := db.Where(
			"`key` = ? AND version_line = ? AND catalog_managed = ? AND catalog_visible = ? AND installable = ?",
			"php", version, true, true, true,
		).Order("recommended DESC, version_order ASC, id ASC").First(&row)
		if result.Error == nil && strings.TrimSpace(row.Version) != "" {
			return strings.TrimSpace(row.Version), nil
		}
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("read PHP version line from Center catalog: %w", result.Error)
		}
	}
	resolved := map[string]string{
		"8.1.x": "8.1.34",
		"8.2.x": "8.2.30",
		"8.3.x": "8.3.30",
	}[version]
	if resolved == "" {
		return "", &InstallParameterError{
			Field:   "version",
			Message: fmt.Sprintf("PHP 版本线 %s 尚未由 Center 发布", version),
		}
	}
	return resolved, nil
}

// validateResolvedInstallPort validates a port only when the resolved
// component declares a port parameter or the caller supplied one explicitly.
// Legacy scripts have no manifest metadata and many of them do not listen on
// a TCP port, so a missing port must remain valid for that compatibility path.
func validateResolvedInstallPort(ctx context.Context, params *input.InstallParams, scriptInfo *script.ScriptInfo) error {
	// PHP-FPM is configured through its Unix socket in the managed PHP
	// component. Ignore an accidental legacy/default TCP port from a generic
	// manifest unless the caller explicitly supplied a port for PHP.
	if params != nil && strings.EqualFold(strings.TrimSpace(params.Key), "php") &&
		!explicitPHPInstallPort(params) {
		return nil
	}
	port := ""
	portDeclared := false
	portRequired := false
	if scriptInfo != nil {
		for _, spec := range scriptInfo.ParameterSpecs {
			if !strings.EqualFold(strings.TrimSpace(spec.Type), "port") {
				continue
			}
			portDeclared = true
			portRequired = spec.Required
			envName := installParameterEnvironmentName(spec)
			port = strings.TrimSpace(scriptInfo.Params[envName])
			break
		}
	}
	if params != nil && strings.TrimSpace(params.Port) != "" {
		port = strings.TrimSpace(params.Port)
		portDeclared = true
	}
	if port == "" {
		if portRequired {
			return &InstallParameterError{Field: "port", Message: "is required"}
		}
		return nil
	}
	if !portDeclared {
		return nil
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return &InstallParameterError{Field: "port", Message: "must be a valid port between 1 and 65535"}
	}
	// A component precheck is authoritative for installation-time port
	// ownership. A generic bind probe cannot distinguish an existing managed
	// listener from an unrelated process or account for an explicit migration
	// flow (for example, Nginx or MySQL takeover). Keep the generic probe only
	// for legacy/component packages that do not provide a precheck action.
	if scriptInfo != nil && strings.TrimSpace(scriptInfo.PrecheckPath) != "" {
		return nil
	}
	return validatePortAvailable(ctx, portNumber)
}

func explicitPHPInstallPort(params *input.InstallParams) bool {
	if params == nil {
		return false
	}
	if strings.TrimSpace(params.Port) != "" {
		return true
	}
	return strings.TrimSpace(installParameterValue(params.Parameters, "port")) != ""
}

// ValidateInstallParams resolves the component package and validates its
// manifest parameters without executing any component action. Version is a
// common install input; port validation follows the resolved package's
// declaration while preserving legacy scripts without manifest metadata.
func (installer *Installer) ValidateInstallParams(ctx context.Context, params *input.InstallParams) error {
	_, err := installer.resolveInstallParams(ctx, params)
	return err
}

// PreviewInstallationParams validates an installation request and returns the
// effective values used by the installer. Values from the signed package
// manifest are included after default resolution. Password values are marked
// sensitive and are never returned.
func PreviewInstallationParams(ctx context.Context, params *input.InstallParams) ([]EffectiveInstallParameter, error) {
	values, _, err := PreviewInstallationPackage(ctx, params)
	return values, err
}

// PreviewInstallationPackage validates the installation and returns the
// immutable package identity that must be embedded in the operation preview.
// Catalog-managed firewalld installations are not allowed to proceed without
// a Center-verified remote/cache package pin.
func PreviewInstallationPackage(ctx context.Context, params *input.InstallParams) ([]EffectiveInstallParameter, scriptregistry.PackagePin, error) {
	if err := ensureCenterCatalogFreshForFirewalld(ctx, params); err != nil {
		return nil, scriptregistry.PackagePin{}, err
	}
	provided := installParameterPresence(params)
	installer := NewInstaller()
	scriptInfo, err := installer.resolveInstallParams(ctx, params)
	if err != nil {
		return nil, scriptregistry.PackagePin{}, err
	}

	values := make([]EffectiveInstallParameter, 0, len(scriptInfo.ParameterSpecs)+4)
	seen := make(map[string]struct{}, len(scriptInfo.ParameterSpecs)+4)
	appendValue := func(key, value, source string, sensitive bool) {
		key = strings.TrimSpace(key)
		if key == "" || strings.TrimSpace(value) == "" && !sensitive {
			return
		}
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		if sensitive {
			value = ""
		}
		values = append(values, EffectiveInstallParameter{
			Key:       key,
			Value:     value,
			Sensitive: sensitive,
			Source:    source,
		})
	}

	appendValue("key", params.Key, "request", false)
	versionSource := "request"
	if !installParameterWasProvided(provided, "version") && !installParameterWasProvided(provided, "software-version") {
		versionSource = "server_resolved"
	}
	appendValue("version", params.Version, versionSource, false)
	portSource := "request"
	if !installParameterWasProvided(provided, "port") {
		portSource = "server_default"
	}
	appendValue("port", params.Port, portSource, false)
	usernameSource := "request"
	if !installParameterWasProvided(provided, "username") {
		usernameSource = "server_default"
	}
	appendValue("username", params.Username, usernameSource, false)

	for _, spec := range scriptInfo.ParameterSpecs {
		envName := installParameterEnvironmentName(spec)
		value := strings.TrimSpace(scriptInfo.Params[envName])
		sensitive := spec.Secret || strings.EqualFold(strings.TrimSpace(spec.Type), "password")
		if value == "" {
			if sensitive && isDatabaseInstallKey(params.Key) && isPasswordParameter(spec.Name) && strings.TrimSpace(params.Pwd) == "" {
				appendValue(spec.Name, "", "server_resolved", true)
			}
			continue
		}
		source := "derived"
		if installParameterWasProvided(provided, spec.Name) {
			source = "request"
		} else if strings.TrimSpace(spec.Default) != "" {
			source = "manifest_default"
		}
		if strings.EqualFold(params.Key, "firewalld") && spec.Name == "software-version" && versionSource == "server_resolved" {
			source = versionSource
		}
		appendValue(spec.Name, value, source, sensitive)
	}
	var pin scriptregistry.PackagePin
	if scriptInfo.PackagePin != nil {
		pin = *scriptInfo.PackagePin
	}
	if strings.EqualFold(strings.TrimSpace(params.Key), "firewalld") &&
		(pin.Component != "firewalld" || (pin.PackageSource != "remote" && pin.PackageSource != "cache") ||
			pin.SoftwareVersion != strings.TrimSpace(params.Version) || pin.PackageSHA256 == "") {
		return nil, scriptregistry.PackagePin{}, errors.New("PACKAGE_RESOLVE_FAILED: firewalld requires a Center-verified package pin")
	}
	return values, pin, nil
}

func ensureCenterCatalogFreshForFirewalld(ctx context.Context, params *input.InstallParams) error {
	if params == nil || !strings.EqualFold(strings.TrimSpace(params.Key), "firewalld") ||
		!app.ONE_CONFIG.ScriptCenter.Enabled || app.DB() == nil {
		return nil
	}
	status, err := GetCatalogStatus()
	if err != nil {
		return fmt.Errorf("CATALOG_STALE: unable to read the Panel software catalog status: %w", err)
	}
	if !status.Stale {
		return nil
	}
	if _, err := SyncCatalogNow(ctx); err != nil {
		return fmt.Errorf("CATALOG_STALE: Panel software catalog refresh failed: %w", err)
	}
	return nil
}

func installParameterEnvironmentName(spec script.ParameterSpec) string {
	if env := strings.TrimSpace(spec.Env); env != "" {
		return env
	}
	return strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(strings.TrimSpace(spec.Name)))
}

func installParameterPresence(params *input.InstallParams) map[string]bool {
	result := make(map[string]bool)
	if params == nil {
		return result
	}
	mark := func(value string) {
		if strings.TrimSpace(value) != "" {
			result[compactInstallParameterName(value)] = true
		}
	}
	if strings.TrimSpace(params.Version) != "" {
		mark("version")
		mark("software-version")
	}
	if strings.TrimSpace(params.Port) != "" {
		mark("port")
	}
	if strings.TrimSpace(params.Username) != "" {
		mark("username")
	}
	if params.Pwd != "" {
		mark("pwd")
		mark("password")
		mark("mysql-password")
	}
	for key, value := range params.Parameters {
		if strings.TrimSpace(value) != "" {
			mark(key)
		}
	}
	return result
}

func installParameterWasProvided(presence map[string]bool, name string) bool {
	target := compactInstallParameterName(name)
	if presence[target] {
		return true
	}
	switch {
	case target == "softwareversion":
		return presence["version"]
	case target == "port":
		if presence["port"] {
			return true
		}
		for key := range presence {
			if strings.HasSuffix(key, "port") {
				return true
			}
		}
		return false
	case target == "mysqlport" || strings.HasSuffix(target, "port"):
		return presence["port"]
	case target == "mysqlpassword" || strings.HasSuffix(target, "password"):
		return presence["pwd"] || presence["password"]
	case target == "runuser" || target == "user" || target == "username":
		return presence["username"] || presence["runuser"]
	default:
		return false
	}
}

func isPasswordParameter(name string) bool {
	target := compactInstallParameterName(name)
	return target == "password" || strings.HasSuffix(target, "password") || target == "pwd"
}

func isDatabaseInstallKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "db", "mysql", "mariadb", "percona":
		return true
	default:
		return false
	}
}

func isManagedMySQLInstallKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "db", "mysql":
		return true
	default:
		return false
	}
}

// ManagedMySQLDatabaseUsername resolves the SQL login account configured for
// the managed MySQL component. It is deliberately separate from Username,
// which represents the Linux service runtime account.
func ManagedMySQLDatabaseUsername(params *input.InstallParams) string {
	if params != nil {
		if username := installParameterValue(
			params.Parameters,
			"mysql-username",
			"mysqlUsername",
			"database-username",
			"databaseUsername",
		); username != "" {
			return username
		}
	}
	return "root"
}

// ValidateManagedMySQLInstallParams validates the aliases shared by the
// legacy flat install request and the Center MySQL component parameters.
// The top-level username is the component's OS runtime account; the SQL
// login account is configured separately through mysql-username.
func ValidateManagedMySQLInstallParams(params *input.InstallParams) error {
	if params == nil || !isManagedMySQLInstallKey(params.Key) {
		return nil
	}
	if username := strings.TrimSpace(params.Username); username != "" && !managedMySQLUsernamePattern.MatchString(username) {
		return &InstallParameterError{
			Field:   "username",
			Message: "MySQL 运行账户必须以小写字母或下划线开头，仅允许小写字母、数字、下划线和连字符，长度为 1-32 个字符",
		}
	}
	if username := ManagedMySQLDatabaseUsername(params); !managedMySQLDatabaseUsernamePattern.MatchString(username) {
		return &InstallParameterError{
			Field:   "mysql-username",
			Message: "MySQL 登录用户必须以小写字母或下划线开头，仅允许小写字母、数字、下划线和连字符，长度为 1-32 个字符",
		}
	}
	if params.Pwd != "" && !managedMySQLPasswordPattern.MatchString(params.Pwd) {
		return &InstallParameterError{
			Field:   "pwd",
			Message: "MySQL 密码必须为 12-128 个字符，仅允许字母、数字及 _ @ % + = : , . ! # ? -",
		}
	}
	return nil
}

// ValidateInstallationParams validates an installation request using the
// same package and parameter rules as the production installer.
func ValidateInstallationParams(ctx context.Context, params *input.InstallParams) error {
	return NewInstaller().ValidateInstallParams(ctx, params)
}

// validatePortAvailable checks the requested TCP port before an installation
// task is persisted. The action scripts still need to check again at runtime,
// because another process can claim the port after this short probe closes its
// temporary listeners.
func validatePortAvailable(ctx context.Context, port int) error {
	if ctx == nil {
		ctx = context.Background()
	}

	address := net.JoinHostPort("", strconv.Itoa(port))
	for _, network := range []string{"tcp4", "tcp6"} {
		listener, err := (&net.ListenConfig{}).Listen(ctx, network, address)
		if err == nil {
			if closeErr := listener.Close(); closeErr != nil {
				return &InstallParameterError{
					Field:   "port",
					Message: fmt.Sprintf("port %d availability could not be confirmed", port),
				}
			}
			continue
		}
		if errors.Is(err, syscall.EADDRINUSE) {
			return &InstallParameterError{
				Field:   "port",
				Message: fmt.Sprintf("port %d is already in use", port),
			}
		}
		if network == "tcp6" && (errors.Is(err, syscall.EAFNOSUPPORT) ||
			errors.Is(err, syscall.EPROTONOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL)) {
			continue
		}
		return &InstallParameterError{
			Field:   "port",
			Message: fmt.Sprintf("port %d availability could not be confirmed", port),
		}
	}
	return nil
}
