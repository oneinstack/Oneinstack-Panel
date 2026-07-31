package buildinfo

import (
	"regexp"
	"runtime"
	"strings"
)

// These values are replaced by release builds through -ldflags.
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitHash = "unknown"
	WebVersion = "dev"
)

const developmentCompatibilityVersion = "0.1.0-dev"

var semanticVersionPattern = regexp.MustCompile(
	`^v?[0-9]+(?:\.[0-9]+){1,2}(?:[-+][0-9A-Za-z.-]+)?$`,
)

// CompatibleVersion returns a semantic version suitable for Center package
// compatibility checks. Local and source builds historically report "dev",
// which is useful for display but is not a valid semantic version.
func CompatibleVersion() string {
	version := strings.TrimSpace(Version)
	if semanticVersionPattern.MatchString(version) {
		return version
	}
	return developmentCompatibilityVersion
}

// Info is the build metadata exposed by the CLI and the authenticated API.
type Info struct {
	Version    string `json:"version"`
	BuildTime  string `json:"buildTime"`
	CommitHash string `json:"commitHash"`
	WebVersion string `json:"webVersion"`
	GoVersion  string `json:"goVersion"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
}

func Current() Info {
	return Info{
		Version:    Version,
		BuildTime:  BuildTime,
		CommitHash: CommitHash,
		WebVersion: WebVersion,
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
	}
}
