package i18n

import (
	"net/http"
	"strings"
)

const (
	LocaleZhCN = "zh-CN"
	LocaleEnUS = "en-US"
)

const (
	MessageOperationSucceeded = "operation.succeeded"
	MessageValidationFailed   = "validation.failed"
	MessageRequestInvalid     = "request.invalid"
)

var supportedLocales = map[string]string{
	"zh":    LocaleZhCN,
	"zh-cn": LocaleZhCN,
	"en":    LocaleEnUS,
	"en-us": LocaleEnUS,
}

var messages = map[string]map[string]string{
	LocaleZhCN: {
		MessageOperationSucceeded: "操作成功",
		MessageValidationFailed:   "输入验证失败",
		MessageRequestInvalid:     "请求参数错误",
	},
	LocaleEnUS: {
		MessageOperationSucceeded: "Operation succeeded",
		MessageValidationFailed:   "Input validation failed",
		MessageRequestInvalid:     "Invalid request parameters",
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
