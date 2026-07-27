package buildinfo

import "runtime"

// These values are replaced by release builds through -ldflags.
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitHash = "unknown"
	WebVersion = "dev"
)

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
