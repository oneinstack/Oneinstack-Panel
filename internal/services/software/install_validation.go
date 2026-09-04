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

	"oneinstack/internal/services/script"
	"oneinstack/router/input"
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

// UserMessage returns a safe, actionable message for errors whose internal
// validation text can be shown directly to an operator. Keep the default
// validation details unchanged for compatibility with existing callers.
func (e *InstallParameterError) UserMessage() string {
	if e == nil {
		return ""
	}

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
		if !strings.HasPrefix(e.Message, "port ") || !strings.HasSuffix(e.Message, item.suffix) {
			continue
		}
		port := strings.TrimSuffix(strings.TrimPrefix(e.Message, "port "), item.suffix)
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
	parameterPort := installParameterValue(params.Parameters, "port", "nginx-port", "nginxPort", "mysql-port", "mysqlPort")

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

// validateResolvedInstallPort validates a port only when the resolved
// component declares a port parameter or the caller supplied one explicitly.
// Legacy scripts have no manifest metadata and many of them do not listen on
// a TCP port, so a missing port must remain valid for that compatibility path.
func validateResolvedInstallPort(ctx context.Context, params *input.InstallParams, scriptInfo *script.ScriptInfo) error {
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
	return validatePortAvailable(ctx, portNumber)
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
	provided := installParameterPresence(params)
	installer := NewInstaller()
	scriptInfo, err := installer.resolveInstallParams(ctx, params)
	if err != nil {
		return nil, err
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
	appendValue("version", params.Version, "request", false)
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
		appendValue(spec.Name, value, source, sensitive)
	}
	return values, nil
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
