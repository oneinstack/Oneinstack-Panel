package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"

	"gorm.io/gorm"
)

const CapabilityClusterHealth = "cluster.health.v1"

const (
	healthHealthy     = "healthy"
	healthWarning     = "warning"
	healthCritical    = "critical"
	healthUnknown     = "unknown"
	healthDisabled    = "disabled"
	healthUnprotected = "unprotected"
)

var healthReasonPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var healthResourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:@-]+$`)
var healthCheckPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
var healthWriteMu sync.Mutex

type HealthObservation struct {
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId"`
	Check        string    `json:"check"`
	Name         string    `json:"name"`
	Target       string    `json:"target,omitempty"`
	Status       string    `json:"status"`
	Reason       string    `json:"reason,omitempty"`
	ObservedAt   time.Time `json:"observedAt"`
}

type HealthReport struct {
	Token   string              `json:"token,omitempty"`
	Version int                 `json:"version"`
	Items   []HealthObservation `json:"items"`
}

type HealthSummary struct {
	Total       int64 `json:"total"`
	Healthy     int64 `json:"healthy"`
	Warning     int64 `json:"warning"`
	Critical    int64 `json:"critical"`
	Unknown     int64 `json:"unknown"`
	Unprotected int64 `json:"unprotected"`
	Disabled    int64 `json:"disabled"`
	Unsupported int64 `json:"unsupportedNodes"`
	Awaiting    int64 `json:"awaitingNodes"`
}

type HealthResourcePage struct {
	Items    []models.ClusterHealthResource `json:"items"`
	Total    int64                          `json:"total"`
	Page     int                            `json:"page"`
	PageSize int                            `json:"pageSize"`
}

type HealthEventPage struct {
	Items    []models.MonitorAlertEvent `json:"items"`
	Total    int64                      `json:"total"`
	Page     int                        `json:"page"`
	PageSize int                        `json:"pageSize"`
}

func validHealthStatus(value string) bool {
	switch value {
	case healthHealthy, healthWarning, healthCritical, healthUnknown, healthDisabled, healthUnprotected:
		return true
	}
	return false
}

func validHealthType(value string) bool {
	switch value {
	case "node", "task", "website", "certificate", "database", "service", "backup":
		return true
	}
	return false
}

func normalizeHealthObservation(item *HealthObservation, now time.Time) error {
	item.ResourceType = strings.TrimSpace(item.ResourceType)
	item.ResourceID = strings.TrimSpace(item.ResourceID)
	item.Check = strings.TrimSpace(item.Check)
	item.Name = strings.TrimSpace(item.Name)
	item.Target = strings.TrimSpace(item.Target)
	item.Reason = strings.TrimSpace(item.Reason)
	if !validHealthType(item.ResourceType) || !validHealthStatus(item.Status) ||
		item.ResourceID == "" || len(item.ResourceID) > 64 || !healthResourceIDPattern.MatchString(item.ResourceID) ||
		item.Check == "" || len(item.Check) > 24 || !healthCheckPattern.MatchString(item.Check) ||
		item.Name == "" || len(item.Name) > 160 || strings.ContainsAny(item.Name, "\r\n\x00") ||
		len(item.Target) > 253 || len(item.Reason) > 64 ||
		(item.Reason != "" && !healthReasonPattern.MatchString(item.Reason)) {
		return errors.New("invalid cluster health observation")
	}
	if item.ObservedAt.IsZero() {
		item.ObservedAt = now
	}
	if item.ObservedAt.Before(now.Add(-10*time.Minute)) || item.ObservedAt.After(now.Add(time.Minute)) {
		return errors.New("cluster health observation time is outside the allowed window")
	}
	if item.Target != "" {
		parsed, err := url.Parse(item.Target)
		if item.ResourceType != "website" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			parsed.Hostname() == "" || parsed.Port() != "" || parsed.User != nil || parsed.Path != "/" ||
			parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Hostname(), ":") {
			return errors.New("invalid cluster health target")
		}
	}
	return nil
}

// ReportHealth accepts only an authenticated node's own read-only observations.
func (m *Manager) ReportHealth(ctx context.Context, report HealthReport) error {
	if report.Version != 1 || len(report.Items) == 0 || len(report.Items) > 500 {
		return errors.New("unsupported cluster health report")
	}
	node, err := m.findByToken(report.Token)
	if err != nil {
		return err
	}
	if !node.Enabled || node.DepartedAt != nil {
		return ErrNodeDisabled
	}
	now := time.Now().UTC()
	seen := make(map[string]struct{}, len(report.Items))
	for i := range report.Items {
		item := &report.Items[i]
		if item.ResourceType == "node" || item.ResourceType == "task" {
			return errors.New("node health report contains controller-owned check")
		}
		if err := normalizeHealthObservation(item, now); err != nil {
			return err
		}
		key := item.ResourceType + ":" + item.ResourceID + ":" + item.Check
		if _, exists := seen[key]; exists {
			return errors.New("duplicate cluster health observation")
		}
		seen[key] = struct{}{}
	}
	for _, item := range report.Items {
		if err := m.applyHealthObservation(ctx, node.ID, item, false); err != nil {
			return err
		}
	}
	return m.reconcileHealthResources(ctx, node.ID, seen)
}

func healthObservationKey(item HealthObservation) string {
	return item.ResourceType + ":" + item.ResourceID + ":" + item.Check
}

func (m *Manager) reconcileHealthResources(ctx context.Context, nodeID uint, seen map[string]struct{}) error {
	healthWriteMu.Lock()
	defer healthWriteMu.Unlock()
	var rows []models.ClusterHealthResource
	if err := m.db.WithContext(ctx).Select("id", "resource_type", "resource_id", "`check`").
		Where("node_id = ? AND resource_type NOT IN ? AND present = ?", nodeID, []string{"node", "task"}, true).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		key := healthObservationKey(HealthObservation{ResourceType: row.ResourceType, ResourceID: row.ResourceID, Check: row.Check})
		if _, ok := seen[key]; ok {
			continue
		}
		if err := m.db.WithContext(ctx).Model(&models.ClusterHealthResource{}).Where("id = ?", row.ID).
			Updates(map[string]any{"present": false, "incident_open": false, "incident_started_at": nil, "failure_count": 0, "recovery_count": 0}).Error; err != nil {
			return err
		}
	}
	return nil
}

func combinedHealth(row models.ClusterHealthResource) string {
	if row.LocalStatus == healthDisabled {
		return healthDisabled
	}
	if row.ResourceType != "website" {
		return row.LocalStatus
	}
	if row.LocalStatus == healthCritical || row.EntryStatus == healthCritical {
		return healthCritical
	}
	if row.LocalStatus == healthWarning || row.EntryStatus == healthWarning {
		return healthWarning
	}
	if row.LocalStatus == healthHealthy && row.EntryStatus == healthHealthy {
		return healthHealthy
	}
	return healthUnknown
}

func (m *Manager) applyHealthObservation(ctx context.Context, nodeID uint, item HealthObservation, entry bool) error {
	now := time.Now().UTC()
	if err := normalizeHealthObservation(&item, now); err != nil {
		return err
	}
	var event *models.MonitorAlertEvent
	var notifyConfigured bool
	healthWriteMu.Lock()
	err := m.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row models.ClusterHealthResource
		err := tx.Where("node_id = ? AND resource_type = ? AND resource_id = ? AND `check` = ?",
			nodeID, item.ResourceType, item.ResourceID, item.Check).First(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = models.ClusterHealthResource{
				NodeID: nodeID, ResourceType: item.ResourceType, ResourceID: item.ResourceID,
				Check: item.Check, Name: item.Name, LocalStatus: healthUnknown, Status: healthUnknown,
				NotificationEnabled: item.ResourceType == "node" || item.ResourceType == "task",
			}
		} else if err != nil {
			return err
		}
		row.Name = item.Name
		row.Present = true
		if !entry {
			if row.Target != item.Target {
				row.EntryStatus = healthUnknown
				row.EntryReason = ""
				row.EntryCheckedAt = nil
			}
			row.Target = item.Target
		}
		if entry {
			row.EntryStatus = item.Status
			row.EntryReason = item.Reason
			row.EntryCheckedAt = &item.ObservedAt
		} else {
			row.LocalStatus = item.Status
			row.LocalReason = item.Reason
			row.ObservedAt = &item.ObservedAt
		}
		row.Status = combinedHealth(row)
		row.Reason = row.LocalReason
		if row.ResourceType == "website" && row.EntryStatus == row.Status && row.EntryStatus != row.LocalStatus {
			row.Reason = row.EntryReason
		}
		bad := row.Status == healthCritical || row.Status == healthWarning
		counted := row.LastCountedAt == nil || now.Sub(*row.LastCountedAt) >= 45*time.Second
		if bad {
			row.RecoveryCount = 0
			if counted {
				row.FailureCount++
			}
		} else if row.Status == healthHealthy {
			row.FailureCount = 0
			if counted {
				row.RecoveryCount++
			}
		} else {
			row.FailureCount, row.RecoveryCount = 0, 0
		}
		if counted {
			row.LastCountedAt = &now
		}
		threshold := 2
		if row.ResourceType == "node" || row.ResourceType == "task" {
			threshold = 1
		}
		eventType := ""
		startedAt := now
		var resolvedAt *time.Time
		if bad && !row.IncidentOpen && row.FailureCount >= threshold {
			row.IncidentOpen = true
			row.IncidentStartedAt = &now
			eventType = models.AlertEventTriggered
		} else if row.Status == healthHealthy && row.IncidentOpen && row.RecoveryCount >= 2 {
			row.IncidentOpen = false
			if row.IncidentStartedAt != nil {
				startedAt = *row.IncidentStartedAt
			}
			resolvedAt = &now
			row.IncidentStartedAt = nil
			eventType = models.AlertEventResolved
		}
		if err := tx.Save(&row).Error; err != nil {
			return err
		}
		if eventType == "" {
			return nil
		}
		severity := "critical"
		if eventType == models.AlertEventResolved {
			severity = "info"
		} else if row.Status == healthWarning {
			severity = "warning"
		}
		ruleName := []rune("集群：" + row.Name)
		if len(ruleName) > 120 {
			ruleName = ruleName[:120]
		}
		subject := fmt.Sprintf("节点 %d", nodeID)
		if nodeID == 0 {
			subject = "控制端"
		}
		action := "出现异常"
		if eventType == models.AlertEventResolved {
			action = "已恢复"
		}
		event = &models.MonitorAlertEvent{
			RuleName: string(ruleName), Metric: "cluster_health", ResourceType: "cluster_health",
			ResourceID: strconv.FormatUint(row.ID, 10), Severity: severity, EventType: eventType,
			StartedAt: startedAt, OccurredAt: now, ResolvedAt: resolvedAt,
			Message: fmt.Sprintf("%s %s %s：%s", subject, row.ResourceType, action, row.Reason),
		}
		if err := tx.Create(event).Error; err != nil {
			return err
		}
		notifyConfigured = row.NotificationEnabled
		return nil
	})
	healthWriteMu.Unlock()
	if err != nil {
		return err
	}
	if notifyConfigured && event != nil && !m.healthNotificationSuppressed(nodeID) {
		if notifier := monitoring.Default(); notifier != nil {
			notifier.DeliverRecordedEvent(ctx, event)
		}
	}
	return nil
}

func (m *Manager) healthNotificationSuppressed(nodeID uint) bool {
	if nodeID == 0 {
		return false
	}
	var node models.ClusterNode
	if err := m.db.Select("lifecycle_status").First(&node, nodeID).Error; err != nil {
		return true
	}
	return node.LifecycleStatus == models.ClusterNodeLifecycleMaintenance ||
		node.LifecycleStatus == models.ClusterNodeLifecycleDraining ||
		node.LifecycleStatus == models.ClusterNodeLifecycleDrained ||
		node.LifecycleStatus == models.ClusterNodeLifecycleDisabled ||
		node.LifecycleStatus == models.ClusterNodeLifecyclePendingDelete
}

func (m *Manager) HealthSummary() (HealthSummary, error) {
	var rows []models.ClusterHealthResource
	if err := m.db.Select("node_id", "resource_type", "status").Where("present = ?", true).Find(&rows).Error; err != nil {
		return HealthSummary{}, err
	}
	var nodes []models.ClusterNode
	if err := m.db.Select("id", "status", "enabled", "departed_at", "capabilities").Find(&nodes).Error; err != nil {
		return HealthSummary{}, err
	}
	offline := make(map[uint]bool, len(nodes))
	reported := make(map[uint]bool, len(nodes))
	result := HealthSummary{Total: int64(len(rows))}
	for _, row := range rows {
		if row.ResourceType != "node" && row.ResourceType != "task" {
			reported[row.NodeID] = true
		}
	}
	for _, node := range nodes {
		offline[node.ID] = node.Status != models.ClusterNodeStatusOnline
		if !node.Enabled || node.DepartedAt != nil {
			continue
		}
		if !containsTaskType(node.Capabilities, CapabilityClusterHealth) {
			result.Unsupported++
		} else if !reported[node.ID] {
			result.Awaiting++
		}
	}
	for _, row := range rows {
		status := row.Status
		if row.ResourceType != "node" && row.ResourceType != "task" && offline[row.NodeID] {
			status = healthUnknown
		}
		switch status {
		case healthHealthy:
			result.Healthy++
		case healthWarning:
			result.Warning++
		case healthCritical:
			result.Critical++
		case healthUnprotected:
			result.Unprotected++
		case healthDisabled:
			result.Disabled++
		default:
			result.Unknown++
		}
	}
	return result, nil
}

func (m *Manager) ListHealthResources(nodeID uint, resourceType, status string, page, pageSize int) (HealthResourcePage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query := m.db.Model(&models.ClusterHealthResource{}).Where("present = ?", true)
	effectiveStatus := "CASE WHEN node_id <> 0 AND resource_type NOT IN ('node','task') AND EXISTS (SELECT 1 FROM cluster_nodes WHERE cluster_nodes.id = cluster_health_resources.node_id AND cluster_nodes.status <> 'online') THEN 'unknown' ELSE status END"
	if nodeID > 0 {
		query = query.Where("node_id = ?", nodeID)
	}
	if resourceType != "" {
		query = query.Where("resource_type = ?", resourceType)
	}
	if status == "attention" {
		query = query.Where(effectiveStatus+" IN ?", []string{healthCritical, healthWarning})
	} else if status != "" {
		query = query.Where(effectiveStatus+" = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return HealthResourcePage{}, err
	}
	var items []models.ClusterHealthResource
	err := query.Order("CASE " + effectiveStatus + " WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 WHEN 'unknown' THEN 2 ELSE 3 END").
		Order("updated_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	if err != nil {
		return HealthResourcePage{}, err
	}
	var nodes []models.ClusterNode
	if err := m.db.Select("id", "status").Find(&nodes).Error; err != nil {
		return HealthResourcePage{}, err
	}
	offline := make(map[uint]bool, len(nodes))
	for _, node := range nodes {
		offline[node.ID] = node.Status != models.ClusterNodeStatusOnline
	}
	for i := range items {
		if items[i].ResourceType != "node" && items[i].ResourceType != "task" && offline[items[i].NodeID] {
			items[i].Status, items[i].Reason = healthUnknown, "node_offline"
		}
	}
	return HealthResourcePage{Items: items, Total: total, Page: page, PageSize: pageSize}, nil
}

func (m *Manager) GetHealthResource(id uint64) (models.ClusterHealthResource, error) {
	var row models.ClusterHealthResource
	if err := m.db.First(&row, id).Error; err != nil {
		return row, err
	}
	if row.NodeID == 0 || row.ResourceType == "node" || row.ResourceType == "task" {
		return row, nil
	}
	var node models.ClusterNode
	if err := m.db.Select("status").First(&node, row.NodeID).Error; err != nil {
		return row, err
	}
	if node.Status != models.ClusterNodeStatusOnline {
		row.Status, row.Reason = healthUnknown, "node_offline"
	}
	return row, nil
}

func (m *Manager) ListHealthEvents(page, pageSize int) (HealthEventPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	query := m.db.Model(&models.MonitorAlertEvent{}).Where("metric = ?", "cluster_health")
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return HealthEventPage{}, err
	}
	var items []models.MonitorAlertEvent
	err := query.Order("occurred_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error
	return HealthEventPage{Items: items, Total: total, Page: page, PageSize: pageSize}, err
}

func (m *Manager) SetHealthNotification(ctx context.Context, id uint64, enabled bool) (models.ClusterHealthResource, error) {
	healthWriteMu.Lock()
	var row models.ClusterHealthResource
	if err := m.db.First(&row, id).Error; err != nil {
		healthWriteMu.Unlock()
		return row, err
	}
	if row.ResourceType == "node" || row.ResourceType == "task" {
		healthWriteMu.Unlock()
		return row, errors.New("node and task notifications are enabled by default")
	}
	if err := m.db.Model(&models.ClusterHealthResource{}).Where("id = ?", id).
		Update("notification_enabled", enabled).Error; err != nil {
		healthWriteMu.Unlock()
		return row, err
	}
	row.NotificationEnabled = enabled
	healthWriteMu.Unlock()
	if enabled && row.IncidentOpen && !m.healthNotificationSuppressed(row.NodeID) {
		var event models.MonitorAlertEvent
		if err := m.db.Where("metric = ? AND resource_id = ? AND event_type = ?", "cluster_health", strconv.FormatUint(row.ID, 10), models.AlertEventTriggered).
			Order("id DESC").First(&event).Error; err == nil {
			var delivered int64
			if err := m.db.Model(&models.NotificationDelivery{}).Where("event_id = ?", event.ID).Count(&delivered).Error; err == nil && delivered == 0 {
				if notifier := monitoring.Default(); notifier != nil {
					notifier.DeliverRecordedEvent(ctx, &event)
				}
			}
		}
	}
	return row, nil
}
