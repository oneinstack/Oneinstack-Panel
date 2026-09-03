package i18n

import "strings"

var monitorSeverityNames = map[string]map[string]string{
	LocaleZhCN: {
		"info":     "信息",
		"warning":  "警告",
		"critical": "严重",
		"fatal":    "致命",
	},
	LocaleEnUS: {
		"info":     "Info",
		"warning":  "Warning",
		"critical": "Critical",
		"fatal":    "Fatal",
	},
}

// LocalizeMonitorSeverity returns the display name for a monitor severity.
// The severity code itself remains unchanged for machine processing.
func LocalizeMonitorSeverity(locale, severity string) string {
	value := strings.TrimSpace(severity)
	if translated, ok := monitorSeverityNames[Canonical(locale)][strings.ToLower(value)]; ok {
		return translated
	}
	return value
}
