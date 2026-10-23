package software

import (
	"fmt"
	"strconv"
	"strings"
)

const phpSecurityMessage = "PHP 安全升级未完成，当前版本可能仍受 CVE-2026-109618 / CVE-2026-109619 影响；请选择已发布的 PHP 8.2.35+、8.3.36+、8.4.27+ 或 8.5.12+，旧分支迁移需显式选择目标版本"

type PHPSecurityUpdateError struct{ Version string }

func (e *PHPSecurityUpdateError) ErrorCode() string { return "PHP_SECURITY_UPDATE_REQUIRED" }
func (e *PHPSecurityUpdateError) Error() string {
	return fmt.Sprintf("%s: PHP %s; %s", e.ErrorCode(), e.Version, phpSecurityMessage)
}
func (e *PHPSecurityUpdateError) UserMessage(locale string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(locale)), "en") {
		return fmt.Sprintf("PHP %s is blocked by the PHP2Shell security policy (CVE-2026-109618 / CVE-2026-109619). Select a published PHP 8.2.35+, 8.3.36+, 8.4.27+ or 8.5.12+ release. Migration from an older branch requires an explicit target version.", e.Version)
	}
	return fmt.Sprintf("PHP %s 已被 PHP2Shell 安全策略禁止安装（CVE-2026-109618 / CVE-2026-109619）；请选择已发布的 PHP 8.2.35+、8.3.36+、8.4.27+ 或 8.5.12+，旧分支迁移需显式选择目标版本", e.Version)
}

// ValidatePHPInstallationVersion applies to creation of runtimes, never to
// maintenance of an existing runtime. It also guards old previews and bundles.
func ValidatePHPInstallationVersion(version string) error {
	version = strings.TrimSpace(version)
	parts := strings.Split(version, ".")
	if len(parts) == 3 && parts[0] == "8" {
		minimum := map[string]int{"2": 35, "3": 36, "4": 27, "5": 12}[parts[1]]
		patch, err := strconv.Atoi(parts[2])
		if minimum != 0 && err == nil && strconv.Itoa(patch) == parts[2] && patch >= minimum {
			return nil
		}
	}
	return &PHPSecurityUpdateError{Version: version}
}

func phpSecurityUpgradeTarget(version string) string {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return ""
	}
	return map[string]string{"8.2": "8.2.35", "8.3": "8.3.36", "8.4": "8.4.27", "8.5": "8.5.12"}[strings.Join(parts[:2], ".")]
}
