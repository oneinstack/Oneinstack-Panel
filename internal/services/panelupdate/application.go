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
	resolveURL := ""
	enabled := center.Enabled
	if manifestURL == "" {
		centerURL := strings.TrimRight(strings.TrimSpace(center.CenterURL), "/")
		if centerURL == "" && app.ONE_CONFIG.ScriptCenter.Enabled {
			centerURL = strings.TrimRight(strings.TrimSpace(app.ONE_CONFIG.ScriptCenter.URL), "/")
		}
		if centerURL != "" {
			resolveURL = centerURL + "/v1/panel/releases/resolve"
		} else {
			manifestURL = center.ManifestURL
		}
	} else {
		enabled = true
	}
	trustedKeys := center.TrustedKeys
	if len(trustedKeys) == 0 {
		trustedKeys = app.ONE_CONFIG.ScriptCenter.TrustedKeys
	}
	instanceID, err := LoadOrCreateInstanceID(app.GetBasePath())
	if err != nil {
		return nil, fmt.Errorf("load panel update instance ID: %w", err)
	}
	return NewManager(Config{
		Enabled: enabled, ManifestURL: manifestURL, ResolveURL: resolveURL,
		InstanceID: instanceID, Channel: center.Channel,
		TrustedKeys:     trustedKeys,
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
