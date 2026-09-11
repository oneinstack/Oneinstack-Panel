package website

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	// Nginx/OpenResty/Tengine and Caddy commonly report a configuration path
	// followed by a colon and line number. The path may be a temporary preview
	// file without a .conf suffix, so do not restrict the basename extension.
	webServerPathLinePattern     = regexp.MustCompile(`(?i)(?:^|[\s(])((?:/[^\s:()]+)|(?:[a-z0-9_.-]+\.conf)):(\d+)(?:(?::|\s+-)\s*(.*))?$`)
	webServerApacheLinePattern   = regexp.MustCompile(`(?i)\bsyntax error on line\s+(\d+)\s+of\s+[^:\r\n]+:?\s*(.*)$`)
	webServerGenericLinePattern  = regexp.MustCompile(`(?i)\b(?:on|at)?\s*line\s+(\d+)\b(?:[:：-]\s*)?(.*)$`)
	webServerAbsolutePathPattern = regexp.MustCompile(`/[^\s"'():]+`)
)

// WebServerConfigValidationError is the normalized validation failure shared
// by preview, direct configuration updates, website publication, and
// certificate deployment. Its Error method is safe for internal task/audit
// messages; the original command output is used only while constructing the
// diagnostic and is never exposed through the API.
type WebServerConfigValidationError struct {
	Engine      string
	Path        string
	Line        int
	Diagnostic  string
	commandErr  error
	restored    bool
	restoreFail bool
}

func (err *WebServerConfigValidationError) Error() string {
	if err == nil {
		return "web server configuration validation failed"
	}
	return "web server configuration validation failed: " + err.safeDetail(false, false)
}

func (err *WebServerConfigValidationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.commandErr
}

// SafeErrorDetail lets the generic core error envelope preserve a structured
// Web Server diagnostic without making core depend on this service package.
// The context error is the complete wrapped error and carries whether the
// previous configuration was restored.
func (err *WebServerConfigValidationError) SafeErrorDetail(contextErr error) string {
	if err == nil {
		return ""
	}
	restored, restoreFail := err.restoreState(contextErr)
	detail := err.safeDetail(restored, restoreFail)
	return detail
}

// WebServerConfigErrorDetail extracts the safe diagnostic from any wrapped
// Web Server validation error. It is used by handlers that build an error
// response without going through core.WrapError.
func WebServerConfigErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	var provider interface {
		SafeErrorDetail(error) string
	}
	if !errors.As(err, &provider) {
		return ""
	}
	return strings.TrimSpace(provider.SafeErrorDetail(err))
}

func (err *WebServerConfigValidationError) restoreState(contextErr error) (bool, bool) {
	if err.restored {
		return true, false
	}
	if contextErr == nil {
		return false, false
	}
	lower := strings.ToLower(contextErr.Error())
	restored := strings.Contains(lower, "previous content restored") ||
		strings.Contains(lower, "previous configuration restored") ||
		strings.Contains(lower, "original configuration restored")
	restoreFail := strings.Contains(lower, "restore failed") ||
		strings.Contains(lower, "restore/reload failed")
	return restored, restoreFail
}

func (err *WebServerConfigValidationError) safeDetail(restored, restoreFail bool) string {
	engine := webServerEngineDisplayName(err.Engine)
	suffix := "预览阶段未写入原配置，请修正后重新预览。"
	if restored {
		suffix = "原配置已自动恢复，请修正后重新预览。"
	}
	if err.restoreFail || restoreFail {
		suffix = "原配置恢复失败，请立即检查配置文件和 Web Server 状态。"
	}

	diagnostic := sanitizeWebServerDiagnostic(err.Diagnostic)
	if err.Line > 0 {
		location := ""
		if path := sanitizeWebServerConfigPath(err.Path); path != "" {
			location = "文件 " + path + " "
		}
		if diagnostic == "" {
			return fmt.Sprintf("%s 配置语法错误：%s第 %d 行。%s", engine, location, err.Line, suffix)
		}
		return fmt.Sprintf("%s 配置语法错误：%s第 %d 行；诊断：%s。%s", engine, location, err.Line, diagnostic, suffix)
	}
	if diagnostic != "" {
		return fmt.Sprintf("%s 配置语法校验失败：%s。%s", engine, diagnostic, suffix)
	}
	return fmt.Sprintf("%s 配置语法校验失败。%s", engine, suffix)
}

func webServerEngineDisplayName(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "nginx":
		return "Nginx"
	case "openresty":
		return "OpenResty"
	case "tengine":
		return "Tengine"
	case "apache", "httpd":
		return "Apache"
	case "caddy":
		return "Caddy"
	default:
		return "Web Server"
	}
}

func newWebServerConfigValidationError(
	ctx context.Context,
	engine, displayPath string,
	output []byte,
	commandErr error,
) error {
	if commandErr == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	line, diagnostic, nativePath := parseWebServerValidationOutput(engine, string(output))
	if strings.TrimSpace(displayPath) == "" {
		displayPath = nativePath
	}
	return &WebServerConfigValidationError{
		Engine:     strings.ToLower(strings.TrimSpace(engine)),
		Path:       displayPath,
		Line:       line,
		Diagnostic: diagnostic,
		commandErr: commandErr,
	}
}

func runWebServerValidationCommand(
	ctx context.Context,
	runner CommandRunner,
	engine, displayPath, command string,
	args ...string,
) error {
	if runner == nil {
		return errors.New("web server command runner is not configured")
	}
	output, err := runner.Run(ctx, command, args...)
	if err == nil {
		return nil
	}
	lowerOutput := strings.ToLower(string(output))
	// Some wrappers return a non-zero status after printing the native success
	// markers. Keep the existing compatibility behavior for validation.
	if strings.Contains(lowerOutput, "syntax is ok") ||
		strings.Contains(lowerOutput, "test is successful") {
		return nil
	}
	return newWebServerConfigValidationError(ctx, engine, displayPath, output, err)
}

func parseWebServerValidationOutput(engine, output string) (int, string, string) {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(engine), "apache") {
			if matches := webServerApacheLinePattern.FindStringSubmatch(line); len(matches) == 3 {
				lineNumber := parsePositiveLineNumber(matches[1])
				message := strings.TrimSpace(matches[2])
				if message == "" && index+1 < len(lines) {
					message = strings.TrimSpace(lines[index+1])
				}
				return lineNumber, cleanWebServerDiagnosticMessage(message), ""
			}
		}
		if matches := webServerPathLinePattern.FindStringSubmatch(line); len(matches) == 4 {
			lineNumber := parsePositiveLineNumber(matches[2])
			message := webServerPathLineDiagnostic(engine, line, matches[0], matches[3])
			return lineNumber, cleanWebServerDiagnosticMessage(message), matches[1]
		}
		if matches := webServerGenericLinePattern.FindStringSubmatch(line); len(matches) == 3 {
			return parsePositiveLineNumber(matches[1]), cleanWebServerDiagnosticMessage(matches[2]), ""
		}
	}

	for _, line := range lines {
		if message := cleanWebServerDiagnosticMessage(line); message != "" {
			return 0, message, ""
		}
	}
	return 0, "", ""
}

func webServerPathLineDiagnostic(engine, line, match, suffix string) string {
	prefix := strings.TrimSpace(line[:strings.Index(line, match)])
	suffix = strings.TrimSpace(suffix)
	if strings.EqualFold(strings.TrimSpace(engine), "caddy") {
		if suffix != "" {
			return suffix
		}
		return prefix
	}
	if marker := strings.LastIndex(prefix, "] "); marker >= 0 {
		prefix = strings.TrimSpace(prefix[marker+2:])
	}
	if marker := strings.LastIndex(prefix, " in "); marker >= 0 {
		prefix = strings.TrimSpace(prefix[:marker])
	}
	if prefix != "" {
		return prefix
	}
	return suffix
}

func parsePositiveLineNumber(value string) int {
	line, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || line <= 0 {
		return 0
	}
	return line
}

func cleanWebServerDiagnosticMessage(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if value == "" {
		return ""
	}
	value = strings.TrimSpace(strings.TrimPrefix(value, "Error:"))
	value = strings.TrimSpace(strings.TrimPrefix(value, "error:"))
	value = webServerAbsolutePathPattern.ReplaceAllString(value, "<配置文件>")
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > 512 {
		value = value[:512]
	}
	return strings.TrimSpace(strings.TrimRight(value, ".。"))
}

func sanitizeWebServerDiagnostic(value string) string {
	return cleanWebServerDiagnosticMessage(value)
}

func sanitizeWebServerConfigPath(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return filepath.Base(value)
	}
	value = strings.TrimPrefix(value, "./")
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return filepath.Base(value)
	}
	return value
}
