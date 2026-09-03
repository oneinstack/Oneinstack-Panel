package i18n

import (
	"fmt"
	"regexp"
)

var panelBackupWebServerMismatchPattern = regexp.MustCompile(
	`^PANEL_BACKUP_WEB_SERVER_MISMATCH: 备份中的 Web Server 为 (.+)，当前运行 Web Server 为 (.+)，请切换到 (.+) 后再恢复$`,
)

func translatePanelBackupWebServerMismatch(text string) (string, bool) {
	matches := panelBackupWebServerMismatchPattern.FindStringSubmatch(text)
	if len(matches) != 4 {
		return "", false
	}
	return fmt.Sprintf(
		"The backup was created for %s, but the current Web Server is %s. Switch to %s before restoring",
		matches[1], matches[2], matches[3],
	), true
}
