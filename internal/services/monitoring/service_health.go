package monitoring

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

const (
	MetricServiceHealth           = "service_health"
	serviceHealthFailureThreshold = 2
	serviceHealthReminderMinutes  = 30
	maxServiceHealthObservations  = 64
)

type ComponentHealthObservation struct {
	Component       string
	DisplayName     string
	SoftwareKey     string
	ServiceName     string
	SoftwareVersion string
	RuntimeVersion  string
	Installed       bool
	Busy            bool
	Healthy         bool
	ServiceState    string
	LoadState       string
	ActiveState     string
	SubState        string
	Error           string
	Severity        string
	CheckedAt       time.Time
}

type ServiceHealthCollector interface {
	CollectServiceHealth(context.Context) ([]ComponentHealthObservation, error)
}

type ServiceHealthCollectorFunc func(context.Context) ([]ComponentHealthObservation, error)

func (function ServiceHealthCollectorFunc) CollectServiceHealth(
	ctx context.Context,
) ([]ComponentHealthObservation, error) {
	return function(ctx)
}

func (manager *Manager) SetServiceHealthCollector(collector ServiceHealthCollector) {
	if manager == nil {
		return
	}
	manager.healthMu.Lock()
	manager.serviceHealth = collector
	manager.healthMu.Unlock()
}

func (manager *Manager) CheckServiceHealth(ctx context.Context) error {
	if manager == nil {
		return errors.New("monitoring manager is not initialized")
	}
	manager.healthMu.Lock()
	defer manager.healthMu.Unlock()
	if manager.serviceHealth == nil {
		return nil
	}
	observations, err := manager.serviceHealth.CollectServiceHealth(ctx)
	if err != nil {
		return fmt.Errorf("collect component service health: %w", err)
	}
	if len(observations) > maxServiceHealthObservations {
		return errors.New("component service health collector returned too many observations")
	}
	seen := make(map[string]struct{}, len(observations))
	states := make([]models.ComponentHealthState, 0, len(observations))
	for index := range observations {
		observation := observations[index]
		if err := normalizeComponentHealthObservation(&observation, manager.now()); err != nil {
			return err
		}
		if _, exists := seen[observation.Component]; exists {
			return fmt.Errorf("duplicate component health observation %s", observation.Component)
		}
		seen[observation.Component] = struct{}{}
		state, event, notify, err := manager.evaluateServiceHealth(&observation)
		if err != nil {
			return err
		}
		states = append(states, *state)
		if event != nil && notify {
			manager.deliver(ctx, event)
		}
	}
	manager.setServiceHealthSnapshot(states)
	return nil
}

func normalizeComponentHealthObservation(
	observation *ComponentHealthObservation,
	now time.Time,
) error {
	observation.Component = strings.ToLower(truncateText(observation.Component, 64))
	observation.DisplayName = truncateText(observation.DisplayName, 120)
	observation.SoftwareKey = strings.ToLower(truncateText(observation.SoftwareKey, 64))
	observation.ServiceName = truncateText(observation.ServiceName, 120)
	observation.SoftwareVersion = truncateText(observation.SoftwareVersion, 64)
	observation.RuntimeVersion = truncateText(observation.RuntimeVersion, 64)
	observation.ServiceState = strings.ToLower(truncateText(observation.ServiceState, 32))
	observation.LoadState = strings.ToLower(truncateText(observation.LoadState, 32))
	observation.ActiveState = strings.ToLower(truncateText(observation.ActiveState, 32))
	observation.SubState = strings.ToLower(truncateText(observation.SubState, 32))
	observation.Error = truncateText(observation.Error, 512)
	if observation.Component == "" || observation.DisplayName == "" ||
		observation.SoftwareKey == "" || observation.ServiceName == "" {
		return errors.New("component service health observation identity is incomplete")
	}
	if observation.CheckedAt.IsZero() {
		observation.CheckedAt = now.UTC()
	} else {
		observation.CheckedAt = observation.CheckedAt.UTC()
	}
	switch observation.Severity {
	case "warning", "critical":
	default:
		observation.Severity = "critical"
	}
	if !observation.Installed {
		observation.Healthy = true
		observation.Busy = false
		observation.ServiceState = "not_installed"
		observation.Error = ""
	}
	if observation.ServiceState == "" {
		if observation.Healthy {
			observation.ServiceState = "running"
		} else {
			observation.ServiceState = "unknown"
		}
	}
	if !observation.Healthy && observation.Error == "" {
		observation.Error = "组件服务状态为 " + observation.ServiceState
	}
	return nil
}

func (manager *Manager) evaluateServiceHealth(
	observation *ComponentHealthObservation,
) (*models.ComponentHealthState, *models.MonitorAlertEvent, bool, error) {
	now := observation.CheckedAt
	var state models.ComponentHealthState
	var event *models.MonitorAlertEvent
	notify := false
	err := manager.db.Transaction(func(tx *gorm.DB) error {
		state = models.ComponentHealthState{
			Component:   observation.Component,
			HealthState: models.MonitorStateNormal,
		}
		result := tx.First(&state, "component = ?", observation.Component)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		state.DisplayName = observation.DisplayName
		state.SoftwareKey = observation.SoftwareKey
		state.ServiceName = observation.ServiceName
		state.SoftwareVersion = observation.SoftwareVersion
		state.RuntimeVersion = observation.RuntimeVersion
		state.Installed = observation.Installed
		state.Busy = observation.Busy
		state.ServiceState = observation.ServiceState
		state.LoadState = observation.LoadState
		state.ActiveState = observation.ActiveState
		state.SubState = observation.SubState
		state.LastCheckedAt = now
		state.LastError = observation.Error

		if observation.Busy {
			if result.Error != nil {
				state.HealthState = models.MonitorStateNormal
			}
			return tx.Save(&state).Error
		}

		unhealthy := observation.Installed && !observation.Healthy
		switch state.HealthState {
		case models.MonitorStateFiring:
			if !unhealthy {
				resolved := now
				started := now
				if state.FiringSince != nil {
					started = *state.FiringSince
				}
				event = newServiceHealthEvent(
					observation,
					models.AlertEventResolved,
					started,
					now,
					&resolved,
				)
				state.HealthState = models.MonitorStateNormal
				state.ConsecutiveFailures = 0
				state.PendingSince = nil
				state.FiringSince = nil
				state.LastNotifiedAt = &now
				notify = true
			} else if reminderDue(
				state.LastNotifiedAt,
				serviceHealthReminderMinutes,
				now,
			) {
				started := now
				if state.FiringSince != nil {
					started = *state.FiringSince
				}
				event = newServiceHealthEvent(
					observation,
					models.AlertEventReminder,
					started,
					now,
					nil,
				)
				state.LastNotifiedAt = &now
				notify = true
			}
		default:
			if !unhealthy {
				state.HealthState = models.MonitorStateNormal
				state.ConsecutiveFailures = 0
				state.PendingSince = nil
				state.FiringSince = nil
			} else {
				if state.HealthState != models.MonitorStatePending {
					state.HealthState = models.MonitorStatePending
					state.ConsecutiveFailures = 0
					state.PendingSince = &now
				}
				state.ConsecutiveFailures++
				if state.ConsecutiveFailures >= serviceHealthFailureThreshold {
					started := now
					if state.PendingSince != nil {
						started = *state.PendingSince
					}
					state.HealthState = models.MonitorStateFiring
					state.FiringSince = &started
					state.LastNotifiedAt = &now
					event = newServiceHealthEvent(
						observation,
						models.AlertEventTriggered,
						started,
						now,
						nil,
					)
					notify = true
				}
			}
		}
		if event != nil {
			if err := tx.Create(event).Error; err != nil {
				return err
			}
		}
		return tx.Save(&state).Error
	})
	if err != nil {
		return nil, nil, false, err
	}
	if stateSilenced(manager.db, observation.Component, now) {
		notify = false
	}
	return &state, event, notify, nil
}

func stateSilenced(database *gorm.DB, component string, now time.Time) bool {
	var state models.ComponentHealthState
	if err := database.Select("silenced_until").
		First(&state, "component = ?", component).Error; err != nil {
		return false
	}
	return state.SilencedUntil != nil && state.SilencedUntil.After(now)
}

func newServiceHealthEvent(
	observation *ComponentHealthObservation,
	eventType string,
	started time.Time,
	occurred time.Time,
	resolved *time.Time,
) *models.MonitorAlertEvent {
	message := observation.DisplayName + " 服务"
	switch eventType {
	case models.AlertEventResolved:
		message += "已恢复运行"
	case models.AlertEventReminder:
		message += "仍然异常"
	default:
		message += "连续探测异常"
	}
	if eventType != models.AlertEventResolved && observation.Error != "" {
		message += "：" + observation.Error
	}
	value := float64(0)
	if observation.Healthy {
		value = 1
	}
	return &models.MonitorAlertEvent{
		RuleID:       0,
		RuleName:     "组件服务：" + observation.DisplayName,
		Metric:       MetricServiceHealth,
		ResourceType: "component_service",
		ResourceID:   observation.Component,
		Severity:     observation.Severity,
		EventType:    eventType,
		Value:        value,
		Threshold:    1,
		StartedAt:    started,
		OccurredAt:   occurred,
		ResolvedAt:   resolved,
		Message:      truncateText(message, 255),
	}
}

func (manager *Manager) ListServiceHealth(
	ctx context.Context,
	includeNotInstalled bool,
) ([]models.ComponentHealthState, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if states, ok := manager.serviceHealthSnapshot(includeNotInstalled); ok {
		return states, nil
	}

	var states []models.ComponentHealthState
	err := manager.db.WithContext(ctx).
		Order("installed DESC").Order("display_name ASC").
		Find(&states).Error
	if err != nil {
		return nil, err
	}
	manager.cacheServiceHealthSnapshot(states)
	return filterServiceHealthStates(states, includeNotInstalled), nil
}

func (manager *Manager) SilenceServiceHealth(
	component string,
	until *time.Time,
) error {
	component = strings.ToLower(strings.TrimSpace(component))
	if component == "" {
		return errors.New("component is required")
	}
	if until != nil {
		value := until.UTC()
		now := manager.now().UTC()
		if value.Before(now) || value.After(now.Add(30*24*time.Hour)) {
			return errors.New("silence expiry must be in the future and within 30 days")
		}
		until = &value
	}
	result := manager.db.Model(&models.ComponentHealthState{}).
		Where("component = ? AND installed = ?", component, true).
		Update("silenced_until", until)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	manager.updateServiceHealthSnapshotSilence(component, until)
	return nil
}

func (manager *Manager) setServiceHealthSnapshot(states []models.ComponentHealthState) {
	ordered := append([]models.ComponentHealthState(nil), states...)
	sortServiceHealthStates(ordered)
	manager.healthSnapshotMu.Lock()
	manager.healthSnapshot = ordered
	manager.healthSnapshotReady = true
	manager.healthSnapshotMu.Unlock()
}

func (manager *Manager) cacheServiceHealthSnapshot(states []models.ComponentHealthState) {
	ordered := append([]models.ComponentHealthState(nil), states...)
	sortServiceHealthStates(ordered)
	manager.healthSnapshotMu.Lock()
	if !manager.healthSnapshotReady {
		manager.healthSnapshot = ordered
		manager.healthSnapshotReady = true
	}
	manager.healthSnapshotMu.Unlock()
}

func (manager *Manager) serviceHealthSnapshot(includeNotInstalled bool) ([]models.ComponentHealthState, bool) {
	manager.healthSnapshotMu.RLock()
	if !manager.healthSnapshotReady {
		manager.healthSnapshotMu.RUnlock()
		return nil, false
	}
	states := append([]models.ComponentHealthState(nil), manager.healthSnapshot...)
	manager.healthSnapshotMu.RUnlock()
	return filterServiceHealthStates(states, includeNotInstalled), true
}

func (manager *Manager) updateServiceHealthSnapshotSilence(component string, until *time.Time) {
	manager.healthSnapshotMu.Lock()
	defer manager.healthSnapshotMu.Unlock()
	if !manager.healthSnapshotReady {
		return
	}
	for index := range manager.healthSnapshot {
		if manager.healthSnapshot[index].Component != component {
			continue
		}
		manager.healthSnapshot[index].SilencedUntil = until
		return
	}
}

func filterServiceHealthStates(
	states []models.ComponentHealthState,
	includeNotInstalled bool,
) []models.ComponentHealthState {
	filtered := make([]models.ComponentHealthState, 0, len(states))
	for _, state := range states {
		if includeNotInstalled || state.Installed {
			filtered = append(filtered, state)
		}
	}
	return filtered
}

func sortServiceHealthStates(states []models.ComponentHealthState) {
	sort.SliceStable(states, func(left, right int) bool {
		if states[left].Installed != states[right].Installed {
			return states[left].Installed
		}
		if states[left].DisplayName != states[right].DisplayName {
			return states[left].DisplayName < states[right].DisplayName
		}
		return states[left].Component < states[right].Component
	})
}
