package artifactpolicy

import (
	"strings"

	"golang.org/x/mod/semver"
)

// SupportsArchiveRetention is the backend capability shared by catalog,
// preview and the cleanup inventory. Older installed packages keep archives.
func SupportsArchiveRetention(component, version string) bool {
	component = strings.ToLower(strings.TrimSpace(component))
	minimum := "v1.0.32"
	switch component {
	case "mysql", "db":
	case "redis":
		minimum = "v1.0.19"
	default:
		return false
	}
	value := "v" + version
	return semver.IsValid(value) && semver.Compare(value, minimum) >= 0
}
