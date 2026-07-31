package monitoring

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type sequenceCollector struct {
	samples []*models.MetricSample
	index   int
}

func (collector *sequenceCollector) Collect(context.Context) (*models.MetricSample, error) {
	if collector.index >= len(collector.samples) {
		return nil, errors.New("no test sample")
	}
	value := *collector.samples[collector.index]
	collector.index++
	return &value, nil
}

type recordingSender struct {
	events []models.MonitorAlertEvent
}

func (sender *recordingSender) Send(
	_ context.Context,
	_ *models.NotificationChannel,
	event *models.MonitorAlertEvent,
) error {
	sender.events = append(sender.events, *event)
	return nil
}

func monitorTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(
		&models.MetricSample{}, &models.MonitorRule{}, &models.MonitorAlertState{},
		&models.MonitorAlertEvent{}, &models.ComponentHealthState{}, &models.NotificationChannel{},
		&models.NotificationDelivery{},
	); err != nil {
		t.Fatal(err)
	}
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x62}, 32)); err != nil {
		t.Fatal(err)
	}
	return database
}

type sequenceServiceHealthCollector struct {
	observations [][]ComponentHealthObservation
	index        int
}

func (collector *sequenceServiceHealthCollector) CollectServiceHealth(
	context.Context,
) ([]ComponentHealthObservation, error) {
	if collector.index >= len(collector.observations) {
		return nil, errors.New("no component health observation")
	}
	result := collector.observations[collector.index]
	collector.index++
	return result, nil
}

func TestComponentServiceHealthTriggersRecoversAndUsesNotifications(t *testing.T) {
	started := time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC)
	unhealthy := func(at time.Time) []ComponentHealthObservation {
		return []ComponentHealthObservation{{
			Component: "nginx", DisplayName: "Nginx", SoftwareKey: "webserver",
			ServiceName: "nginx", SoftwareVersion: "1.28.2", Installed: true,
			Healthy: false, ServiceState: "failed", LoadState: "loaded",
			ActiveState: "failed", SubState: "failed", Error: "systemd 状态异常",
			Severity: "critical", CheckedAt: at,
		}}
	}
	collector := &sequenceServiceHealthCollector{observations: [][]ComponentHealthObservation{
		unhealthy(started),
		unhealthy(started.Add(time.Minute)),
		{{
			Component: "nginx", DisplayName: "Nginx", SoftwareKey: "webserver",
			ServiceName: "nginx", SoftwareVersion: "1.28.2", RuntimeVersion: "1.28.2",
			Installed: true, Healthy: true, ServiceState: "running", LoadState: "loaded",
			ActiveState: "active", SubState: "running", Severity: "critical",
			CheckedAt: started.Add(2 * time.Minute),
		}},
	}}
	sender := &recordingSender{}
	manager := newTestManager(t, &sequenceCollector{}, sender)
	manager.SetServiceHealthCollector(collector)
	if err := manager.db.Create(&models.NotificationChannel{
		ID: "service-channel", Name: "operations", Type: "webhook", Enabled: true,
		ConfigEncrypted: "not-used-by-recording-sender",
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := manager.CheckServiceHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state models.ComponentHealthState
	if err := manager.db.First(&state, "component = ?", "nginx").Error; err != nil {
		t.Fatal(err)
	}
	if state.HealthState != models.MonitorStatePending ||
		state.ConsecutiveFailures != 1 {
		t.Fatalf("unexpected pending service health state: %#v", state)
	}
	if err := manager.CheckServiceHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.db.First(&state, "component = ?", "nginx").Error; err != nil {
		t.Fatal(err)
	}
	if state.HealthState != models.MonitorStateFiring ||
		state.ConsecutiveFailures != 2 {
		t.Fatalf("unexpected firing service health state: %#v", state)
	}
	summary, err := manager.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServiceFiringCount != 1 || summary.ServicePendingCount != 0 {
		t.Fatalf("unexpected firing service summary: %#v", summary)
	}
	if err := manager.CheckServiceHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.db.First(&state, "component = ?", "nginx").Error; err != nil {
		t.Fatal(err)
	}
	if state.HealthState != models.MonitorStateNormal ||
		state.ConsecutiveFailures != 0 {
		t.Fatalf("unexpected recovered service health state: %#v", state)
	}
	summary, err = manager.Summary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.ServiceFiringCount != 0 || summary.ServicePendingCount != 0 {
		t.Fatalf("unexpected recovered service summary: %#v", summary)
	}
	var events []models.MonitorAlertEvent
	if err := manager.db.Order("occurred_at ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].EventType != models.AlertEventTriggered ||
		events[1].EventType != models.AlertEventResolved ||
		events[0].ResourceType != "component_service" ||
		events[0].ResourceID != "nginx" {
		t.Fatalf("unexpected component health events: %#v", events)
	}
	if len(sender.events) != 2 {
		t.Fatalf("component health delivery count = %d, want 2", len(sender.events))
	}
}

func TestComponentServiceHealthSilenceSuppressesDelivery(t *testing.T) {
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	manager := newTestManager(t, &sequenceCollector{}, &recordingSender{})
	manager.now = func() time.Time { return now }
	if err := manager.db.Create(&models.ComponentHealthState{
		Component: "redis", DisplayName: "Redis", SoftwareKey: "redis",
		ServiceName: "redis", Installed: true, HealthState: models.MonitorStateNormal,
		ServiceState: "running", LastCheckedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Hour)
	if err := manager.SilenceServiceHealth("redis", &until); err != nil {
		t.Fatal(err)
	}
	manager.SetServiceHealthCollector(&sequenceServiceHealthCollector{
		observations: [][]ComponentHealthObservation{
			{{
				Component: "redis", DisplayName: "Redis", SoftwareKey: "redis",
				ServiceName: "redis", Installed: true, Healthy: false,
				ServiceState: "stopped", Severity: "warning", CheckedAt: now,
			}},
			{{
				Component: "redis", DisplayName: "Redis", SoftwareKey: "redis",
				ServiceName: "redis", Installed: true, Healthy: false,
				ServiceState: "stopped", Severity: "warning", CheckedAt: now.Add(time.Minute),
			}},
		},
	})
	if err := manager.CheckServiceHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckServiceHealth(context.Background()); err != nil {
		t.Fatal(err)
	}
	var events int64
	if err := manager.db.Model(&models.MonitorAlertEvent{}).Count(&events).Error; err != nil {
		t.Fatal(err)
	}
	var deliveries int64
	if err := manager.db.Model(&models.NotificationDelivery{}).Count(&deliveries).Error; err != nil {
		t.Fatal(err)
	}
	if events != 1 || deliveries != 0 {
		t.Fatalf("silenced component health events=%d deliveries=%d", events, deliveries)
	}
}

func newTestManager(
	t *testing.T,
	collector Collector,
	sender Sender,
) *Manager {
	t.Helper()
	manager, err := NewManager(
		monitorTestDB(t), collector, sender, 30, 365,
		"* * * * *", "20 4 * * *",
	)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestAlertStateTriggersAfterConsecutiveSamplesAndRecovers(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	collector := &sequenceCollector{samples: []*models.MetricSample{
		{CapturedAt: started, CPUPercent: 95},
		{CapturedAt: started.Add(time.Minute), CPUPercent: 96},
		{CapturedAt: started.Add(2 * time.Minute), CPUPercent: 94},
		{CapturedAt: started.Add(3 * time.Minute), CPUPercent: 79},
	}}
	sender := &recordingSender{}
	manager := newTestManager(t, collector, sender)
	channel, err := manager.CreateChannel(ChannelInput{
		Name: "operations", Type: "webhook", Enabled: true,
		WebhookURL: "https://alerts.example.com/hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if channel.TargetHint != "alerts.example.com" {
		t.Fatalf("target hint = %q", channel.TargetHint)
	}
	rule, err := manager.CreateRule(RuleInput{
		Name: "CPU high", Metric: MetricCPU, Operator: "gte",
		Threshold: 90, RecoveryThreshold: 80, ConsecutiveSamples: 2,
		CooldownMinutes: 60, Severity: "critical", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	for range collector.samples {
		if _, err := manager.CollectNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var events []models.MonitorAlertEvent
	if err := manager.db.Order("occurred_at ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].EventType != models.AlertEventTriggered ||
		events[1].EventType != models.AlertEventResolved {
		t.Fatalf("unexpected alert events: %#v", events)
	}
	if len(sender.events) != 2 {
		t.Fatalf("notification count = %d, want 2", len(sender.events))
	}
	var state models.MonitorAlertState
	if err := manager.db.First(&state, "rule_id = ?", rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if state.State != models.MonitorStateNormal || state.ConsecutiveBreaches != 0 {
		t.Fatalf("unexpected recovered state: %#v", state)
	}
}

func TestTaskFailureUsesConfiguredNotificationChannels(t *testing.T) {
	sender := &recordingSender{}
	manager := newTestManager(t, &sequenceCollector{}, sender)
	if err := manager.db.Create(&models.NotificationChannel{
		ID: "cron-channel", Name: "operations", Type: "webhook", Enabled: true,
		ConfigEncrypted: "not-used-by-recording-sender",
	}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	err := manager.NotifyTaskFailure(context.Background(), &models.CronJob{
		ID: 7, Name: "nightly backup",
	}, &models.JobExecution{
		ID: 11, CronJobID: 7, StartTime: now.Add(-time.Minute), EndTime: now,
		Status: "failed", ErrorCode: "COMMAND_FAILED", ExitCode: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sender.events) != 1 ||
		sender.events[0].Metric != "cron_task" ||
		sender.events[0].EventType != models.AlertEventTriggered {
		t.Fatalf("unexpected task failure notification: %#v", sender.events)
	}
	var events int64
	var deliveries int64
	_ = manager.db.Model(&models.MonitorAlertEvent{}).Count(&events).Error
	_ = manager.db.Model(&models.NotificationDelivery{}).Count(&deliveries).Error
	if events != 1 || deliveries != 1 {
		t.Fatalf("events=%d deliveries=%d", events, deliveries)
	}
}

func TestFiringRuleRemindsInsideHysteresisBand(t *testing.T) {
	started := time.Date(2026, 7, 26, 12, 30, 0, 0, time.UTC)
	collector := &sequenceCollector{samples: []*models.MetricSample{
		{CapturedAt: started, CPUPercent: 95},
		{CapturedAt: started.Add(11 * time.Minute), CPUPercent: 85},
	}}
	sender := &recordingSender{}
	manager := newTestManager(t, collector, sender)
	if _, err := manager.CreateChannel(ChannelInput{
		Name: "operations", Type: "webhook", Enabled: true,
		WebhookURL: "https://alerts.example.com/hook",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CreateRule(RuleInput{
		Name: "CPU high", Metric: MetricCPU, Operator: "gte",
		Threshold: 90, RecoveryThreshold: 80, ConsecutiveSamples: 1,
		CooldownMinutes: 10, Severity: "critical", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	for range collector.samples {
		if _, err := manager.CollectNow(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	var events []models.MonitorAlertEvent
	if err := manager.db.Order("occurred_at ASC").Find(&events).Error; err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].EventType != models.AlertEventReminder {
		t.Fatalf("hysteresis reminder events: %#v", events)
	}
}

func TestSilencedRuleStillPersistsEventWithoutDelivery(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	collector := &sequenceCollector{samples: []*models.MetricSample{
		{CapturedAt: now, MemoryPercent: 95},
	}}
	sender := &recordingSender{}
	manager := newTestManager(t, collector, sender)
	manager.now = func() time.Time { return now }
	if _, err := manager.CreateChannel(ChannelInput{
		Name: "operations", Type: "webhook", Enabled: true,
		WebhookURL: "https://alerts.example.com/hook",
	}); err != nil {
		t.Fatal(err)
	}
	rule, err := manager.CreateRule(RuleInput{
		Name: "memory high", Metric: MetricMemory, Operator: "gt",
		Threshold: 90, RecoveryThreshold: 80, ConsecutiveSamples: 1,
		CooldownMinutes: 10, Severity: "warning", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	until := now.Add(time.Hour)
	if err := manager.SilenceRule(rule.ID, &until); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CollectNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := manager.db.Model(&models.MonitorAlertEvent{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(sender.events) != 0 {
		t.Fatalf("silenced result: events=%d deliveries=%d", count, len(sender.events))
	}
}

func TestNotificationConfigIsEncryptedAndPrivateTargetsAreRejected(t *testing.T) {
	manager := newTestManager(t, &sequenceCollector{}, &recordingSender{})
	channel, err := manager.CreateChannel(ChannelInput{
		Name: "secure hook", Type: "webhook", Enabled: true,
		WebhookURL: "https://hooks.example.com/private-token", Secret: "signing-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored models.NotificationChannel
	if err := manager.db.First(&stored, "id = ?", channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !utils.IsEncryptedCredential(stored.ConfigEncrypted) ||
		bytes.Contains([]byte(stored.ConfigEncrypted), []byte("private-token")) ||
		bytes.Contains([]byte(stored.ConfigEncrypted), []byte("signing-secret")) {
		t.Fatal("notification credentials were not encrypted")
	}
	if _, err := manager.CreateChannel(ChannelInput{
		Name: "unsafe", Type: "webhook", Enabled: true,
		WebhookURL: "https://127.0.0.1/hook",
	}); err == nil {
		t.Fatal("loopback webhook target was accepted")
	}
	if _, err := manager.CreateChannel(ChannelInput{
		Name: "reserved", Type: "webhook", Enabled: true,
		WebhookURL: "https://192.0.2.1/hook",
	}); err == nil {
		t.Fatal("reserved webhook target was accepted")
	}
	updated, err := manager.UpdateChannel(channel.ID, ChannelInput{
		Name: "secure hook", Type: "webhook", Enabled: true, ClearSecret: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.HasSecret {
		t.Fatal("notification secret was not explicitly cleared")
	}
}

func TestListRulesIncludesCurrentState(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 30, 0, 0, time.UTC)
	collector := &sequenceCollector{samples: []*models.MetricSample{
		{CapturedAt: now, DiskPercent: 96},
	}}
	manager := newTestManager(t, collector, &recordingSender{})
	rule, err := manager.CreateRule(RuleInput{
		Name: "disk high", Metric: MetricDisk, Operator: "gte",
		Threshold: 90, RecoveryThreshold: 80, ConsecutiveSamples: 1,
		CooldownMinutes: 30, Severity: "critical", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CollectNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	rules, err := manager.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].ID != rule.ID ||
		rules[0].CurrentState != models.MonitorStateFiring ||
		rules[0].LastValue != 96 || rules[0].LastEvaluatedAt == nil {
		t.Fatalf("rule state not attached: %#v", rules)
	}
}

func TestCleanupUsesIndependentMetricAndAlertRetention(t *testing.T) {
	manager := newTestManager(t, &sequenceCollector{}, &recordingSender{})
	now := time.Date(2026, 7, 26, 14, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	oldMetric := models.MetricSample{CapturedAt: now.AddDate(0, 0, -31)}
	recentMetric := models.MetricSample{CapturedAt: now.AddDate(0, 0, -1)}
	if err := manager.db.Create(&[]models.MetricSample{oldMetric, recentMetric}).Error; err != nil {
		t.Fatal(err)
	}
	oldEvent := models.MonitorAlertEvent{
		RuleID: 1, RuleName: "old", Metric: MetricCPU, Severity: "warning",
		EventType: models.AlertEventTriggered, StartedAt: now.AddDate(-2, 0, 0),
		OccurredAt: now.AddDate(-2, 0, 0),
	}
	if err := manager.db.Create(&oldEvent).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.Cleanup(); err != nil {
		t.Fatal(err)
	}
	var metricCount, eventCount int64
	_ = manager.db.Model(&models.MetricSample{}).Count(&metricCount).Error
	_ = manager.db.Model(&models.MonitorAlertEvent{}).Count(&eventCount).Error
	if metricCount != 1 || eventCount != 0 {
		t.Fatalf("cleanup counts: metrics=%d events=%d", metricCount, eventCount)
	}
}

func TestMetricsReturnsNewestSamplesInChronologicalOrder(t *testing.T) {
	manager := newTestManager(t, &sequenceCollector{}, &recordingSender{})
	started := time.Date(2026, 7, 26, 15, 0, 0, 0, time.UTC)
	for index := 0; index < 4; index++ {
		if err := manager.db.Create(&models.MetricSample{
			CapturedAt: started.Add(time.Duration(index) * time.Minute),
			CPUPercent: float64(index),
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	samples, err := manager.Metrics(time.Time{}, time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0].CPUPercent != 2 || samples[1].CPUPercent != 3 {
		t.Fatalf("unexpected metric window: %#v", samples)
	}
}
