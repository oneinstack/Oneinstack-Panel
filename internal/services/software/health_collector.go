package software

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"

	"gorm.io/gorm"
)

type ComponentHealthCollector struct {
	database *gorm.DB
	timeout  time.Duration
}

func NewComponentHealthCollector(database *gorm.DB) *ComponentHealthCollector {
	return &ComponentHealthCollector{database: database, timeout: 12 * time.Second}
}

func (collector *ComponentHealthCollector) CollectServiceHealth(
	ctx context.Context,
) ([]monitoring.ComponentHealthObservation, error) {
	if collector == nil || collector.database == nil {
		return nil, errors.New("component health database is not initialized")
	}
	definitions := SupportedComponentServices()
	keys := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		keys = append(keys, definition.SoftwareKey)
	}
	var installedRows []models.Software
	if err := collector.database.
		Where("`key` IN ? AND installed = ?", keys, true).
		Order("install_time DESC").
		Find(&installedRows).Error; err != nil {
		return nil, fmt.Errorf("list installed component services: %w", err)
	}
	installedByKey := make(map[string]models.Software, len(installedRows))
	for _, row := range installedRows {
		if _, exists := installedByKey[row.Key]; exists {
			continue
		}
		installedByKey[row.Key] = row
	}
	var activeTasks []models.SoftwareTask
	if err := collector.database.
		Where("status IN ?", models.ActiveSoftwareTaskStatuses()).
		Order("created_at DESC").
		Find(&activeTasks).Error; err != nil {
		return nil, fmt.Errorf("list active component tasks: %w", err)
	}
	busy := make(map[string]bool, len(activeTasks))
	for _, task := range activeTasks {
		busy[strings.ToLower(strings.TrimSpace(task.Component))] = true
	}

	observations := make([]monitoring.ComponentHealthObservation, len(definitions))
	var probes sync.WaitGroup
	for index, definition := range definitions {
		checkedAt := time.Now().UTC()
		observation := monitoring.ComponentHealthObservation{
			Component:    definition.Component,
			DisplayName:  definition.DisplayName,
			SoftwareKey:  definition.SoftwareKey,
			ServiceName:  definition.ServiceName,
			Healthy:      true,
			ServiceState: "not_installed",
			Severity:     componentHealthSeverity(definition.Component),
			CheckedAt:    checkedAt,
		}
		installed, exists := installedByKey[definition.SoftwareKey]
		if !exists || !componentMatchesDefinition(installed.Component, definition.Component) {
			observations[index] = observation
			continue
		}
		observation.Installed = true
		observation.SoftwareVersion = strings.TrimSpace(installed.InstallVersion)
		if observation.SoftwareVersion == "" {
			observation.SoftwareVersion = strings.TrimSpace(installed.Version)
		}
		if busy[definition.Component] {
			observation.Busy = true
			observation.ServiceState = "transitioning"
			observations[index] = observation
			continue
		}
		if observation.SoftwareVersion == "" {
			observation.Healthy = false
			observation.ServiceState = "unknown"
			observation.Error = "组件安装版本缺失"
			observations[index] = observation
			continue
		}
		probes.Add(1)
		go func(
			index int,
			definition ComponentServiceDefinition,
			observation monitoring.ComponentHealthObservation,
		) {
			defer probes.Done()
			probeContext, cancel := context.WithTimeout(ctx, collector.timeout)
			defer cancel()
			probe, err := NewInstaller().InspectServiceLocal(
				probeContext,
				definition.Component,
				observation.SoftwareVersion,
			)
			observation.CheckedAt = time.Now().UTC()
			if err != nil {
				observation.Healthy = false
				observation.ServiceState = "unknown"
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
					observation.Error = "本机服务状态探测超时"
				} else {
					observation.Error = err.Error()
				}
				observations[index] = observation
				return
			}
			observation.RuntimeVersion = probe.RuntimeVersion
			observation.LoadState = probe.LoadState
			observation.ActiveState = probe.ActiveState
			observation.SubState = probe.SubState
			observation.ServiceState = componentProbeState(probe.ActiveState)
			observation.Healthy = probe.LoadState == "loaded" && probe.ActiveState == "active"
			if !observation.Healthy {
				observation.Error = fmt.Sprintf(
					"systemd 状态为 %s/%s/%s",
					probe.LoadState,
					probe.ActiveState,
					probe.SubState,
				)
			}
			observations[index] = observation
		}(index, definition, observation)
	}
	probes.Wait()
	return observations, nil
}

func componentMatchesDefinition(recorded string, expected string) bool {
	recorded = strings.ToLower(strings.TrimSpace(recorded))
	return recorded == "" || recorded == strings.ToLower(strings.TrimSpace(expected))
}

func componentHealthSeverity(component string) string {
	switch component {
	case "nginx", "mysql":
		return "critical"
	default:
		return "warning"
	}
}

func componentProbeState(activeState string) string {
	switch activeState {
	case "active":
		return "running"
	case "inactive":
		return "stopped"
	case "failed":
		return "failed"
	case "activating", "deactivating", "reloading":
		return "transitioning"
	default:
		return "unknown"
	}
}
