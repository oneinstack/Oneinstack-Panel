package i18n

import (
	"fmt"
	"net/http"
	"strings"
)

const (
	LocaleZhCN = "zh-CN"
	LocaleEnUS = "en-US"
)

const (
	MessageOperationSucceeded          = "operation.succeeded"
	MessageValidationFailed            = "validation.failed"
	MessageRequestInvalid              = "request.invalid"
	MessagePasswordResetRequiresChange = "password.reset.requires_change"
)

var supportedLocales = map[string]string{
	"zh":    LocaleZhCN,
	"zh-cn": LocaleZhCN,
	"en":    LocaleEnUS,
	"en-us": LocaleEnUS,
}

var messages = map[string]map[string]string{
	LocaleZhCN: {
		MessageOperationSucceeded:          "操作成功",
		MessageValidationFailed:            "输入验证失败",
		MessageRequestInvalid:              "请求参数错误",
		MessagePasswordResetRequiresChange: "密码已重置，用户下次登录需修改密码",
	},
	LocaleEnUS: {
		MessageOperationSucceeded:          "Operation succeeded",
		MessageValidationFailed:            "Input validation failed",
		MessageRequestInvalid:              "Invalid request parameters",
		MessagePasswordResetRequiresChange: "Password reset. The user must change it at the next login.",
	},
}

// FromRequest selects the first supported language from Accept-Language.
// Chinese remains the compatibility fallback for existing clients.
func FromRequest(r *http.Request) string {
	if r == nil {
		return LocaleZhCN
	}
	for _, item := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		language := strings.ToLower(strings.TrimSpace(strings.SplitN(item, ";", 2)[0]))
		if locale, ok := supportedLocales[language]; ok {
			return locale
		}
		if separator := strings.IndexByte(language, '-'); separator > 0 {
			if locale, ok := supportedLocales[language[:separator]]; ok {
				return locale
			}
		}
	}
	return LocaleZhCN
}

func IsSupported(locale string) bool {
	_, ok := messages[Canonical(locale)]
	return ok
}

func Canonical(locale string) string {
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return LocaleZhCN
	}
	if canonical, ok := supportedLocales[strings.ToLower(locale)]; ok {
		return canonical
	}
	return LocaleZhCN
}

func Message(locale, key, fallback string) string {
	if translated, ok := messages[Canonical(locale)][key]; ok {
		return translated
	}
	return fallback
}

// ParseCLILanguage validates a language selected for terminal output. It is
// intentionally separate from Canonical: web requests keep their historical
// Chinese fallback, while an invalid CLI value must be rejected explicitly.
func ParseCLILanguage(value string) (string, error) {
	value = strings.TrimSpace(value)
	if canonical, ok := supportedLocales[strings.ToLower(value)]; ok {
		return canonical, nil
	}
	return "", fmt.Errorf("unsupported language %q; supported languages: %s, %s (aliases: en, zh)", value, LocaleEnUS, LocaleZhCN)
}

// ResolveCLILanguage applies the CLI language precedence: flag, environment,
// persisted configuration, and finally English.
func ResolveCLILanguage(flagValue, environmentValue, persistedValue string) (string, error) {
	for _, value := range []string{flagValue, environmentValue, persistedValue} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		return ParseCLILanguage(value)
	}
	return LocaleEnUS, nil
}

const (
	CLIShowLanguage        = "cli.language.show"
	CLISetLanguage         = "cli.language.set"
	CLIInvalidLanguage     = "cli.language.invalid"
	CLIUserExists          = "cli.user.exists"
	CLIEntryEnabled        = "cli.panel.entry.enabled"
	CLIEntryDisabled       = "cli.panel.entry.disabled"
	CLIHTTPSAddress        = "cli.panel.https.address"
	CLIHelpLanguage        = "cli.language.help"
	CLIProductName         = "cli.product.name"
	CLIVersion             = "cli.version"
	CLIBuildTime           = "cli.build.time"
	CLICommitHash          = "cli.commit.hash"
	CLIWebVersion          = "cli.web.version"
	CLIGoVersion           = "cli.go.version"
	CLIPlatform            = "cli.platform"
	CLIInitialAdminCreated = "cli.initial_admin.created"
	CLIUsername            = "cli.username"
	CLIPassword            = "cli.password"
)

var cliMessages = map[string]map[string]string{
	LocaleZhCN: {
		CLIShowLanguage:        "当前 CLI 语言：%s",
		CLISetLanguage:         "CLI 语言已切换为 %s。",
		CLIInvalidLanguage:     "不支持的语言 %q；支持的语言：%s、%s（简写：en、zh）",
		CLIUserExists:          "管理员用户已经存在，跳过初始化。",
		CLIEntryEnabled:        "安全入口已开启，请使用以下地址访问面板：",
		CLIEntryDisabled:       "安全入口未开启，当前面板访问地址：",
		CLIHTTPSAddress:        "HTTPS 地址：",
		CLIHelpLanguage:        "查看或切换 CLI 输出语言",
		CLIProductName:         "Oneinstack 面板",
		CLIVersion:             "版本",
		CLIBuildTime:           "构建时间",
		CLICommitHash:          "提交哈希",
		CLIWebVersion:          "Web 版本",
		CLIGoVersion:           "Go 版本",
		CLIPlatform:            "平台",
		CLIInitialAdminCreated: "管理员用户已创建",
		CLIUsername:            "用户名",
		CLIPassword:            "密码",
	},
	LocaleEnUS: {
		CLIShowLanguage:        "Current CLI language: %s",
		CLISetLanguage:         "CLI language switched to %s.",
		CLIInvalidLanguage:     "Unsupported language %q; supported languages: %s, %s (aliases: en, zh)",
		CLIUserExists:          "The administrator user already exists; initialization was skipped.",
		CLIEntryEnabled:        "The secure entry is enabled. Use the following URL to access the panel:",
		CLIEntryDisabled:       "The secure entry is disabled. Current panel access URL:",
		CLIHTTPSAddress:        "HTTPS URL:",
		CLIHelpLanguage:        "Show or change the CLI output language",
		CLIProductName:         "Oneinstack Panel",
		CLIVersion:             "Version",
		CLIBuildTime:           "Build Time",
		CLICommitHash:          "Commit Hash",
		CLIWebVersion:          "Web Version",
		CLIGoVersion:           "Go Version",
		CLIPlatform:            "Platform",
		CLIInitialAdminCreated: "Administrator user created",
		CLIUsername:            "Username",
		CLIPassword:            "Password",
	},
}

func CLIMessage(locale, key string) string {
	if translated, ok := cliMessages[Canonical(locale)][key]; ok {
		return translated
	}
	return key
}
