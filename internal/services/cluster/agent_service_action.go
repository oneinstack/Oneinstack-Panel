package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"oneinstack/app"
	"oneinstack/internal/models"
	softwareService "oneinstack/internal/services/software"
)

func (a *Agent) executeServiceAction(ctx context.Context, task *models.ClusterTask) (json.RawMessage, error) {
	var payload serviceActionTaskPayload
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return nil, errors.New("service action payload is invalid")
	}
	payload.Component = normalizeServiceActionComponent(payload.Component)
	payload.Action = strings.ToLower(strings.TrimSpace(payload.Action))
	if payload.Component == "" || !softwareService.IsServiceAction(payload.Action) {
		return nil, errors.New("service action payload is unsupported")
	}
	switch task.Type {
	case TaskServiceActionPreflight:
		return executeServiceActionPreflight(ctx, payload)
	case TaskServiceActionExecute:
		return executeServiceActionFixed(ctx, task.ID, payload)
	default:
		return nil, errors.New("service action task type is unsupported")
	}
}

func executeServiceActionPreflight(ctx context.Context, payload serviceActionTaskPayload) (json.RawMessage, error) {
	preview, err := softwareService.PreviewServiceLifecycle(ctx, payload.Component, payload.Action, nil)
	if err != nil {
		return nil, fmt.Errorf("resolve component lifecycle package: %w", err)
	}
	if preview.PackagePin == nil || strings.TrimSpace(preview.SoftwareVersion) == "" {
		return nil, errors.New("component lifecycle package pin is unavailable")
	}
	probe, err := softwareService.NewInstaller().InspectService(ctx, preview.Component, preview.SoftwareVersion)
	if err != nil {
		return nil, fmt.Errorf("probe managed service: %w", err)
	}
	if !containsString(probe.AvailableActions, payload.Action) {
		return nil, errors.New("component package does not support the requested service action")
	}
	definition, err := softwareService.ResolveServiceComponent(app.DB(), preview.Component)
	if err != nil {
		return nil, fmt.Errorf("resolve managed service: %w", err)
	}
	result := serviceActionPreflightResult{
		Component: preview.Component, DisplayName: definition.DisplayName, Action: payload.Action,
		Version: preview.SoftwareVersion, ServiceName: probe.ServiceName, ActiveState: probe.ActiveState,
		PackagePin: *preview.PackagePin,
	}
	return json.Marshal(result)
}

func executeServiceActionFixed(ctx context.Context, taskID uint64, payload serviceActionTaskPayload) (json.RawMessage, error) {
	if payload.PackagePin == nil || strings.TrimSpace(payload.Version) == "" ||
		payload.PackagePin.Component != payload.Component || payload.PackagePin.SoftwareVersion != payload.Version ||
		strings.TrimSpace(payload.PackagePin.PackageSHA256) == "" {
		return nil, errors.New("fixed component lifecycle package pin is invalid")
	}
	definition, err := softwareService.ResolveServiceComponent(app.DB(), payload.Component)
	if err != nil {
		return nil, fmt.Errorf("resolve managed service: %w", err)
	}
	logPath := filepath.Join(app.GetBasePath(), "logs", "cluster-service-actions", fmt.Sprintf("%d.log", taskID))
	if _, err := softwareService.NewInstaller().ServiceActionTaskWithPackage(
		ctx, payload.Component, payload.Version, payload.Action, payload.PackagePin, logPath, nil,
	); err != nil {
		return nil, fmt.Errorf("execute fixed component lifecycle action: %w", err)
	}
	return json.Marshal(serviceActionExecutionResult{
		Component: payload.Component, DisplayName: definition.DisplayName, Action: payload.Action,
		Version: payload.Version, ServiceName: definition.ServiceName,
		PackageID: payload.PackagePin.ResolvedVersion, PackageSHA256: payload.PackagePin.PackageSHA256,
	})
}
