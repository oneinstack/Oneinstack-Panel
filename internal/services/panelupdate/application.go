package panelupdate

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/buildinfo"
)

func NewApplicationManager(manifestOverride string) (*Manager, error) {
	center := app.ONE_CONFIG.UpdateCenter
	manifestURL := strings.TrimSpace(manifestOverride)
	enabled := center.Enabled
	if manifestURL == "" {
		manifestURL = center.ManifestURL
	} else {
		enabled = true
	}
	return NewManager(Config{
		Enabled: enabled, ManifestURL: manifestURL, Channel: center.Channel,
		TrustedKeys:     center.TrustedKeys,
		RequestTimeout:  time.Duration(center.RequestTimeoutSeconds) * time.Second,
		MaxPackageBytes: center.MaxPackageBytes, MaxExpandedBytes: center.MaxExpandedBytes,
		HealthTimeout:   time.Duration(center.HealthTimeoutSeconds) * time.Second,
		BackupRetention: center.BackupRetention,
		InstallDir:      strings.TrimSuffix(app.GetBasePath(), "/"),
		HealthURL:       "http://127.0.0.1:" + app.ONE_CONFIG.System.Port + "/health/ready",
		CurrentVersion:  buildinfo.Current().Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
	})
}

func LoadApplicationManager(manifestOverride string) (*Manager, error) {
	if _, err := app.LoadConfig(); err != nil {
		return nil, fmt.Errorf("load update configuration: %w", err)
	}
	return NewApplicationManager(manifestOverride)
}
