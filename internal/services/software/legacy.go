package software

import (
	"context"
	"errors"
	"time"

	"oneinstack/app"
	"oneinstack/internal/services/legacyoneinstack"
	"oneinstack/internal/services/script"
	"oneinstack/router/input"
)

func inspectLegacyService(ctx context.Context, component string) (ComponentServiceProbe, bool, error) {
	row, err := legacyoneinstack.Installed(app.DB(), component)
	if err != nil || row == nil {
		return ComponentServiceProbe{}, false, err
	}
	c, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
	if err != nil {
		return ComponentServiceProbe{}, true, err
	}
	probe := ComponentServiceProbe{Component: c.ID, ServiceName: c.Service, RecordedVersion: c.Version, RuntimeVersion: legacyoneinstack.RuntimeVersion(ctx, c), Ownership: "external", PackageSource: legacyoneinstack.Origin, AvailableActions: []string{}}
	if c.Service == "" {
		probe.LoadState = "not-found"
		probe.ActiveState = "unknown"
		return probe, true, nil
	}
	v, err := legacyoneinstack.VerifyService(ctx, c)
	if err != nil {
		return probe, true, err
	}
	probe.LoadState = v["LoadState"]
	probe.ActiveState = v["ActiveState"]
	probe.SubState = v["SubState"]
	probe.UnitFileState = v["UnitFileState"]
	probe.CanReload = v["CanReload"] == "yes"
	probe.AvailableActions = []string{"start", "stop", "restart"}
	if probe.CanReload {
		probe.AvailableActions = append(probe.AvailableActions, "reload")
	}
	return probe, true, nil
}

func (installer *Installer) legacyServiceAction(ctx context.Context, component, version, action, logPath string, observer script.ExecutionObserver) (string, bool, error) {
	row, err := legacyoneinstack.Installed(app.DB(), component)
	if err != nil || row == nil {
		return "", false, err
	}
	c, err := legacyoneinstack.DecodeComponent(row.LegacyMetadata)
	if err != nil {
		return "", true, err
	}
	v, err := legacyoneinstack.VerifyService(ctx, c)
	if err != nil {
		return "", true, err
	}
	if actual := legacyoneinstack.RuntimeVersion(ctx, c); actual == "" || actual != version {
		return "", true, errors.New("RUNTIME_VERSION_DRIFT: refresh OneinStack discovery before controlling the changed runtime")
	}
	if action == "reload" && v["CanReload"] != "yes" {
		return "", true, errors.New("service does not support reload")
	}
	info := &script.ScriptInfo{Name: c.ID, Content: legacyoneinstack.ServiceScript, Source: legacyoneinstack.Origin, ActionName: action, Version: c.Version, Params: map[string]string{"LEGACY_ACTION": action, "LEGACY_SERVICE": c.Service, "LEGACY_COMPONENT": c.ID, "LEGACY_BINARY": c.Binary, "LEGACY_CONFIG": c.Config, "LEGACY_INSTALL_DIR": c.InstallDir}, Timeouts: map[string]time.Duration{action: 120 * time.Second}}
	name, err := installer.scriptManager.ExecuteScriptTask(ctx, info, &input.InstallParams{Key: row.Key, Version: version}, logPath, observer)
	return name, true, err
}
