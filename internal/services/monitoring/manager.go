package monitoring

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/models"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const (
	MetricCPU        = "cpu"
	MetricMemory     = "memory"
	MetricDisk       = "disk"
	MetricLoad1      = "load1"
	MetricNetReceive = "network_receive"
	MetricNetSend    = "network_send"
	MetricDiskRead   = "disk_read"
	MetricDiskWrite  = "disk_write"
)

const monitorHistoryTargetPoints = 1440

type HistoryPoint struct {
	CapturedAt time.Time `json:"capturedAt"`
	Value      float64   `json:"value"`
}

type HistorySeries struct {
	Group  string         `json:"group"`
	Key    string         `json:"key"`
	Label  string         `json:"label"`
	Unit   string         `json:"unit,omitempty"`
	Points []HistoryPoint `json:"points"`
}

type HistoryRange struct {
	From          time.Time `json:"from"`
	To            time.Time `json:"to"`
	BucketSeconds int64     `json:"bucketSeconds"`
	SampleCount   int       `json:"sampleCount"`
	BucketCount   int       `json:"bucketCount"`
}

type HistoryResponse struct {
	Range  HistoryRange    `json:"range"`
	Series []HistorySeries `json:"series"`
}

type historySeriesDefinition struct {
	Group string
	Key   string
	Label string
	Unit  string
	Value func(*models.MetricSample) float64
}

var metricHistorySeriesDefinitions = []historySeriesDefinition{
	{Group: "cpu", Key: "cpuPercent", Label: "CPU 使用率", Unit: "%", Value: func(sample *models.MetricSample) float64 {
		return sample.CPUPercent
	}},
	{Group: "memory", Key: "memoryPercent", Label: "内存使用率", Unit: "%", Value: func(sample *models.MetricSample) float64 {
		return sample.MemoryPercent
	}},
	{Group: "load", Key: "load1", Label: "1 分钟负载", Value: func(sample *models.MetricSample) float64 {
		return sample.Load1
	}},
	{Group: "load", Key: "load5", Label: "5 分钟负载", Value: func(sample *models.MetricSample) float64 {
		return sample.Load5
	}},
	{Group: "load", Key: "load15", Label: "15 分钟负载", Value: func(sample *models.MetricSample) float64 {
		return sample.Load15
	}},
	{Group: "network", Key: "networkReceiveBps", Label: "网络接收", Unit: "B/s", Value: func(sample *models.MetricSample) float64 {
		return sample.NetworkReceiveBPS
	}},
	{Group: "network", Key: "networkSendBps", Label: "网络发送", Unit: "B/s", Value: func(sample *models.MetricSample) float64 {
		return sample.NetworkSendBPS
	}},
	{Group: "disk", Key: "diskPercent", Label: "磁盘使用率", Unit: "%", Value: func(sample *models.MetricSample) float64 {
		return sample.DiskPercent
	}},
	{Group: "disk", Key: "diskReadBps", Label: "磁盘读取", Unit: "B/s", Value: func(sample *models.MetricSample) float64 {
		return sample.DiskReadBPS
	}},
	{Group: "disk", Key: "diskWriteBps", Label: "磁盘写入", Unit: "B/s", Value: func(sample *models.MetricSample) float64 {
		return sample.DiskWriteBPS
	}},
}

type RuleInput struct {
	Name               string  `json:"name"`
	Metric             string  `json:"metric"`
	Operator           string  `json:"operator"`
	Threshold          float64 `json:"threshold"`
	RecoveryThreshold  float64 `json:"recoveryThreshold"`
	ConsecutiveSamples int     `json:"consecutiveSamples"`
	CooldownMinutes    int     `json:"cooldownMinutes"`
	Severity           string  `json:"severity"`
	Enabled            bool    `json:"enabled"`
}

type EventFilter struct {
	Page, PageSize int
	RuleID         uint
	EventType      string
	Severity       string
	ResourceType   string
	ResourceID     string
}

type EventPage struct {
	Data       []models.MonitorAlertEvent `json:"data"`
	Total      int64                      `json:"total"`
	Page       int                        `json:"page"`
	PageSize   int                        `json:"pageSize"`
	TotalPages int                        `json:"totalPages"`
}

type DeliveryPage struct {
	Data       []models.NotificationDelivery `json:"data"`
	Total      int64                         `json:"total"`
	Page       int                           `json:"page"`
	PageSize   int                           `json:"pageSize"`
	TotalPages int                           `json:"totalPages"`
}

type Summary struct {
	Latest              *models.MetricSample `json:"latest,omitempty"`
	RuleCount           int64                `json:"ruleCount"`
	EnabledRules        int64                `json:"enabledRules"`
	FiringCount         int64                `json:"firingCount"`
	PendingCount        int64                `json:"pendingCount"`
	ServiceFiringCount  int64                `json:"serviceFiringCount"`
	ServicePendingCount int64                `json:"servicePendingCount"`
	Last24Hours         int64                `json:"last24Hours"`
}

type Manager struct {
	db                  *gorm.DB
	collector           Collector
	sender              Sender
	retentionDays       int
	alertRetention      int
	scheduler           *cron.Cron
	now                 func() time.Time
	mu                  sync.Mutex
	healthMu            sync.Mutex
	healthSnapshotMu    sync.RWMutex
	healthSnapshot      []models.ComponentHealthState
	healthSnapshotReady bool
	serviceHealth       ServiceHealthCollector
	background          sync.WaitGroup
	startOnce           sync.Once
	stopOnce            sync.Once
}

var defaultManager struct {
	sync.RWMutex
	value *Manager
}

func NewManager(
	db *gorm.DB,
	collector Collector,
	sender Sender,
	retentionDays, alertRetentionDays int,
	sampleSchedule, cleanupSchedule string,
) (*Manager, error) {
	if db == nil || collector == nil || sender == nil {
		return nil, errors.New("monitoring dependencies are not configured")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("metric retention must be between 1 and 3650 days")
	}
	if alertRetentionDays < 1 || alertRetentionDays > 3650 {
		return nil, errors.New("alert retention must be between 1 and 3650 days")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	manager := &Manager{
		db: db, collector: collector, sender: sender,
		retentionDays: retentionDays, alertRetention: alertRetentionDays,
		scheduler: scheduler, now: time.Now,
	}
	if _, err := scheduler.AddFunc(sampleSchedule, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, collectErr := manager.CollectNow(ctx); collectErr != nil {
			log.Printf("monitor metric collection failed: %v", collectErr)
		}
		if healthErr := manager.CheckServiceHealth(ctx); healthErr != nil {
			log.Printf("component service health collection failed: %v", healthErr)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid monitor sample schedule: %w", err)
	}
	if _, err := scheduler.AddFunc(cleanupSchedule, func() {
		if cleanupErr := manager.Cleanup(); cleanupErr != nil {
			log.Printf("monitor retention cleanup failed: %v", cleanupErr)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid monitor cleanup schedule: %w", err)
	}
	return manager, nil
}

func ConfigureDefault(manager *Manager) {
	defaultManager.Lock()
	defaultManager.value = manager
	defaultManager.Unlock()
}

func Default() *Manager {
	defaultManager.RLock()
	defer defaultManager.RUnlock()
	return defaultManager.value
}

func ClearDefault(manager *Manager) {
	defaultManager.Lock()
	if defaultManager.value == manager {
		defaultManager.value = nil
	}
	defaultManager.Unlock()
}

func (manager *Manager) Start() {
	if manager == nil {
		return
	}
	manager.startOnce.Do(func() {
		manager.scheduler.Start()
		manager.background.Add(1)
		go func() {
			defer manager.background.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if _, err := manager.CollectNow(ctx); err != nil {
				log.Printf("initial monitor metric collection failed: %v", err)
			}
			if err := manager.CheckServiceHealth(ctx); err != nil {
				log.Printf("initial component service health collection failed: %v", err)
			}
		}()
	})
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil {
		return nil
	}
	var stopped context.Context
	manager.stopOnce.Do(func() { stopped = manager.scheduler.Stop() })
	if stopped == nil {
		return nil
	}
	finished := make(chan struct{})
	go func() {
		<-stopped.Done()
		manager.background.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (manager *Manager) CollectNow(ctx context.Context) (*models.MetricSample, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	sample, err := manager.collector.Collect(ctx)
	if err != nil {
		return nil, err
	}
	sample.CapturedAt = sample.CapturedAt.UTC().Truncate(time.Second)
	if sample.CapturedAt.IsZero() {
		sample.CapturedAt = manager.now().UTC().Truncate(time.Second)
	}
	if err := manager.db.Create(sample).Error; err != nil {
		return nil, fmt.Errorf("persist metric sample: %w", err)
	}
	if err := manager.evaluate(ctx, sample); err != nil {
		return nil, err
	}
	return sample, nil
}

func (manager *Manager) evaluate(ctx context.Context, sample *models.MetricSample) error {
	var rules []models.MonitorRule
	if err := manager.db.Where("enabled = ?", true).Order("id ASC").Find(&rules).Error; err != nil {
		return err
	}
	for index := range rules {
		event, notify, err := manager.evaluateRule(&rules[index], sample)
		if err != nil {
			return err
		}
		if event != nil && notify {
			manager.deliver(ctx, event)
		}
	}
	return nil
}

func (manager *Manager) evaluateRule(
	rule *models.MonitorRule,
	sample *models.MetricSample,
) (*models.MonitorAlertEvent, bool, error) {
	value := metricValue(sample, rule.Metric)
	now := sample.CapturedAt
	var event *models.MonitorAlertEvent
	notify := false
	err := manager.db.Transaction(func(tx *gorm.DB) error {
		state := models.MonitorAlertState{RuleID: rule.ID, State: models.MonitorStateNormal}
		result := tx.First(&state, "rule_id = ?", rule.ID)
		if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return result.Error
		}
		state.LastValue = value
		state.LastEvaluatedAt = now
		breached := comparison(value, rule.Operator, rule.Threshold)
		recovered := recoveryComparison(value, rule.Operator, rule.RecoveryThreshold)
		switch state.State {
		case models.MonitorStateFiring:
			if !breached && recovered {
				resolved := now
				started := now
				if state.FiringSince != nil {
					started = *state.FiringSince
				}
				event = newAlertEvent(rule, models.AlertEventResolved, value, started, now, &resolved)
				state.State = models.MonitorStateNormal
				state.ConsecutiveBreaches = 0
				state.PendingSince = nil
				state.FiringSince = nil
				state.LastNotifiedAt = &now
				notify = true
			} else if reminderDue(state.LastNotifiedAt, rule.CooldownMinutes, now) {
				started := now
				if state.FiringSince != nil {
					started = *state.FiringSince
				}
				event = newAlertEvent(rule, models.AlertEventReminder, value, started, now, nil)
				state.LastNotifiedAt = &now
				notify = true
			}
		default:
			if !breached {
				state.State = models.MonitorStateNormal
				state.ConsecutiveBreaches = 0
				state.PendingSince = nil
			} else {
				if state.State != models.MonitorStatePending {
					state.State = models.MonitorStatePending
					state.ConsecutiveBreaches = 0
					state.PendingSince = &now
				}
				state.ConsecutiveBreaches++
				if state.ConsecutiveBreaches >= rule.ConsecutiveSamples {
					started := now
					if state.PendingSince != nil {
						started = *state.PendingSince
					}
					state.State = models.MonitorStateFiring
					state.FiringSince = &started
					state.LastNotifiedAt = &now
					event = newAlertEvent(rule, models.AlertEventTriggered, value, started, now, nil)
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
		return nil, false, err
	}
	if rule.SilencedUntil != nil && rule.SilencedUntil.After(now) {
		notify = false
	}
	return event, notify, nil
}

func (manager *Manager) deliver(ctx context.Context, event *models.MonitorAlertEvent) {
	var channels []models.NotificationChannel
	if err := manager.db.Where("enabled = ?", true).Order("created_at ASC").Find(&channels).Error; err != nil {
		log.Printf("list alert notification channels: %v", err)
		return
	}
	for index := range channels {
		channel := &channels[index]
		delivery := models.NotificationDelivery{
			EventID: event.ID, ChannelID: channel.ID, ChannelName: channel.Name,
			Status: "success", AttemptedAt: manager.now().UTC(),
		}
		sendContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := manager.sender.Send(sendContext, channel, event)
		cancel()
		if err != nil {
			delivery.Status = "failed"
			delivery.Error = sanitizeError(err)
		}
		if saveErr := manager.db.Create(&delivery).Error; saveErr != nil {
			log.Printf("persist alert delivery: %v", saveErr)
		}
	}
}

// NotifyTaskFailure records a one-shot operational alert and delivers it
// through the same encrypted notification channels as threshold alerts.
func (manager *Manager) NotifyTaskFailure(
	ctx context.Context,
	job *models.CronJob,
	execution *models.JobExecution,
) error {
	if manager == nil || job == nil || execution == nil {
		return errors.New("task failure notification is incomplete")
	}
	if execution.Status != "failed" && execution.Status != "timeout" {
		return errors.New("only failed or timed out tasks can trigger notifications")
	}
	occurredAt := execution.EndTime.UTC()
	if occurredAt.IsZero() {
		occurredAt = manager.now().UTC()
	}
	severity := "warning"
	if execution.Status == "failed" {
		severity = "critical"
	}
	event := &models.MonitorAlertEvent{
		RuleID: 0, RuleName: "计划任务：" + truncateText(job.Name, 108),
		Metric: "cron_task", Severity: severity, EventType: models.AlertEventTriggered,
		Value: float64(execution.ExitCode), Threshold: 0,
		StartedAt: execution.StartTime.UTC(), OccurredAt: occurredAt,
		Message: truncateText(fmt.Sprintf(
			"计划任务 #%d 执行 #%d %s（%s）",
			job.ID, execution.ID, execution.Status, execution.ErrorCode,
		), 255),
	}
	if err := manager.db.Create(event).Error; err != nil {
		return fmt.Errorf("persist task failure alert: %w", err)
	}
	manager.deliver(ctx, event)
	return nil
}

func truncateText(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}

func (manager *Manager) CreateRule(input RuleInput) (*models.MonitorRule, error) {
	if err := validateRule(input); err != nil {
		return nil, err
	}
	rule := &models.MonitorRule{
		Name: strings.TrimSpace(input.Name), Metric: input.Metric, Operator: input.Operator,
		Threshold: input.Threshold, RecoveryThreshold: input.RecoveryThreshold,
		ConsecutiveSamples: input.ConsecutiveSamples, CooldownMinutes: input.CooldownMinutes,
		Severity: input.Severity, Enabled: input.Enabled,
	}
	if err := manager.db.Create(rule).Error; err != nil {
		return nil, err
	}
	return rule, nil
}

func (manager *Manager) UpdateRule(id uint, input RuleInput) (*models.MonitorRule, error) {
	if id == 0 {
		return nil, errors.New("rule id is required")
	}
	if err := validateRule(input); err != nil {
		return nil, err
	}
	var rule models.MonitorRule
	if err := manager.db.First(&rule, id).Error; err != nil {
		return nil, err
	}
	err := manager.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&rule).Updates(map[string]interface{}{
			"name": strings.TrimSpace(input.Name), "metric": input.Metric,
			"operator": input.Operator, "threshold": input.Threshold,
			"recovery_threshold":  input.RecoveryThreshold,
			"consecutive_samples": input.ConsecutiveSamples,
			"cooldown_minutes":    input.CooldownMinutes,
			"severity":            input.Severity, "enabled": input.Enabled,
		}).Error; err != nil {
			return err
		}
		return tx.Delete(&models.MonitorAlertState{}, "rule_id = ?", id).Error
	})
	if err != nil {
		return nil, err
	}
	return manager.GetRule(id)
}

func (manager *Manager) DeleteRule(id uint) error {
	return manager.db.Transaction(func(tx *gorm.DB) error {
		if result := tx.Delete(&models.MonitorRule{}, id); result.Error != nil {
			return result.Error
		} else if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return tx.Delete(&models.MonitorAlertState{}, "rule_id = ?", id).Error
	})
}

func (manager *Manager) SilenceRule(id uint, until *time.Time) error {
	if until != nil {
		value := until.UTC()
		if value.Before(manager.now().UTC()) || value.After(manager.now().UTC().Add(30*24*time.Hour)) {
			return errors.New("silence expiry must be in the future and within 30 days")
		}
		until = &value
	}
	result := manager.db.Model(&models.MonitorRule{}).Where("id = ?", id).
		Update("silenced_until", until)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (manager *Manager) GetRule(id uint) (*models.MonitorRule, error) {
	var rule models.MonitorRule
	err := manager.db.First(&rule, id).Error
	if err == nil {
		err = manager.attachRuleStates([]*models.MonitorRule{&rule})
	}
	return &rule, err
}

func (manager *Manager) ListRules() ([]models.MonitorRule, error) {
	var rules []models.MonitorRule
	if err := manager.db.Order("created_at DESC").Find(&rules).Error; err != nil {
		return nil, err
	}
	pointers := make([]*models.MonitorRule, 0, len(rules))
	for index := range rules {
		pointers = append(pointers, &rules[index])
	}
	return rules, manager.attachRuleStates(pointers)
}

func (manager *Manager) attachRuleStates(rules []*models.MonitorRule) error {
	if len(rules) == 0 {
		return nil
	}
	ids := make([]uint, 0, len(rules))
	for _, rule := range rules {
		rule.CurrentState = models.MonitorStateNormal
		ids = append(ids, rule.ID)
	}
	var states []models.MonitorAlertState
	if err := manager.db.Where("rule_id IN ?", ids).Find(&states).Error; err != nil {
		return err
	}
	byRule := make(map[uint]models.MonitorAlertState, len(states))
	for _, state := range states {
		byRule[state.RuleID] = state
	}
	for _, rule := range rules {
		state, exists := byRule[rule.ID]
		if !exists {
			continue
		}
		rule.CurrentState = state.State
		rule.LastValue = state.LastValue
		evaluated := state.LastEvaluatedAt
		rule.LastEvaluatedAt = &evaluated
		rule.FiringSince = state.FiringSince
	}
	return nil
}

func (manager *Manager) Metrics(from, to time.Time, limit int) ([]models.MetricSample, error) {
	if limit < 1 {
		limit = 2000
	}
	if limit > 10000 {
		limit = 10000
	}
	query := manager.db.Order("captured_at DESC").Order("id DESC").Limit(limit)
	if !from.IsZero() {
		query = query.Where("captured_at >= ?", from.UTC())
	}
	if !to.IsZero() {
		query = query.Where("captured_at <= ?", to.UTC())
	}
	var samples []models.MetricSample
	err := query.Find(&samples).Error
	for left, right := 0, len(samples)-1; left < right; left, right = left+1, right-1 {
		samples[left], samples[right] = samples[right], samples[left]
	}
	return samples, err
}

func (manager *Manager) History(from, to time.Time) (*HistoryResponse, error) {
	from = from.UTC().Truncate(time.Second)
	to = to.UTC().Truncate(time.Second)
	if from.IsZero() || to.IsZero() {
		return nil, errors.New("history range is required")
	}
	if to.Before(from) {
		return nil, errors.New("history range end must not be before start")
	}
	if to.Sub(from) > 31*24*time.Hour {
		return nil, errors.New("history range must not exceed 31 days")
	}

	query := manager.db.Order("captured_at ASC").Order("id ASC")
	query = query.Where("captured_at >= ?", from)
	query = query.Where("captured_at <= ?", to)

	var samples []models.MetricSample
	if err := query.Find(&samples).Error; err != nil {
		return nil, err
	}

	bucketSeconds := int64(math.Ceil(to.Sub(from).Seconds() / monitorHistoryTargetPoints))
	if bucketSeconds < int64(time.Minute/time.Second) {
		bucketSeconds = int64(time.Minute / time.Second)
	}
	bucketDuration := time.Duration(bucketSeconds) * time.Second
	series := make([]HistorySeries, len(metricHistorySeriesDefinitions))
	seriesByKey := make(map[string]*HistorySeries, len(metricHistorySeriesDefinitions))
	for index, definition := range metricHistorySeriesDefinitions {
		series[index] = HistorySeries{
			Group: definition.Group,
			Key:   definition.Key,
			Label: definition.Label,
			Unit:  definition.Unit,
		}
		seriesByKey[definition.Key] = &series[index]
	}

	type historyBucket struct {
		start  time.Time
		count  int
		totals map[string]float64
	}

	buckets := make([]historyBucket, 0)
	var current *historyBucket
	var currentStart time.Time
	for index := range samples {
		sample := &samples[index]
		bucketOffset := int64(sample.CapturedAt.Sub(from) / bucketDuration)
		if bucketOffset < 0 {
			bucketOffset = 0
		}
		startAt := from.Add(time.Duration(bucketOffset) * bucketDuration)
		if current == nil || !startAt.Equal(currentStart) {
			buckets = append(buckets, historyBucket{
				start:  startAt,
				totals: make(map[string]float64, len(metricHistorySeriesDefinitions)),
			})
			current = &buckets[len(buckets)-1]
			currentStart = startAt
		}
		current.count++
		for _, definition := range metricHistorySeriesDefinitions {
			current.totals[definition.Key] += definition.Value(sample)
		}
	}

	for _, bucket := range buckets {
		for _, definition := range metricHistorySeriesDefinitions {
			item := seriesByKey[definition.Key]
			item.Points = append(item.Points, HistoryPoint{
				CapturedAt: bucket.start,
				Value:      bucket.totals[definition.Key] / float64(bucket.count),
			})
		}
	}

	return &HistoryResponse{
		Range: HistoryRange{
			From:          from,
			To:            to,
			BucketSeconds: bucketSeconds,
			SampleCount:   len(samples),
			BucketCount:   len(buckets),
		},
		Series: series,
	}, nil
}

func (manager *Manager) Events(filter EventFilter) (*EventPage, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize < 1 {
		filter.PageSize = 20
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}
	query := manager.db.Model(&models.MonitorAlertEvent{})
	if filter.RuleID > 0 {
		query = query.Where("rule_id = ?", filter.RuleID)
	}
	if filter.EventType != "" {
		query = query.Where("event_type = ?", filter.EventType)
	}
	if filter.Severity != "" {
		query = query.Where("severity = ?", filter.Severity)
	}
	if filter.ResourceType != "" {
		query = query.Where("resource_type = ?", filter.ResourceType)
	}
	if filter.ResourceID != "" {
		query = query.Where("resource_id = ?", filter.ResourceID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var events []models.MonitorAlertEvent
	err := query.Order("occurred_at DESC").
		Offset((filter.Page - 1) * filter.PageSize).Limit(filter.PageSize).
		Find(&events).Error
	return &EventPage{
		Data: events, Total: total, Page: filter.Page, PageSize: filter.PageSize,
		TotalPages: int(math.Ceil(float64(total) / float64(filter.PageSize))),
	}, err
}

func (manager *Manager) Deliveries(page, pageSize int, status string) (*DeliveryPage, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query := manager.db.Model(&models.NotificationDelivery{})
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var deliveries []models.NotificationDelivery
	err := query.Order("attempted_at DESC").Offset((page - 1) * pageSize).
		Limit(pageSize).Find(&deliveries).Error
	return &DeliveryPage{
		Data: deliveries, Total: total, Page: page, PageSize: pageSize,
		TotalPages: int(math.Ceil(float64(total) / float64(pageSize))),
	}, err
}

func (manager *Manager) Summary() (*Summary, error) {
	summary := &Summary{}
	var latest models.MetricSample
	if err := manager.db.Order("captured_at DESC").Order("id DESC").First(&latest).Error; err == nil {
		summary.Latest = &latest
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := manager.db.Model(&models.MonitorRule{}).
		Count(&summary.RuleCount).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.MonitorRule{}).Where("enabled = ?", true).
		Count(&summary.EnabledRules).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.MonitorAlertState{}).
		Where("state = ?", models.MonitorStateFiring).Count(&summary.FiringCount).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.MonitorAlertState{}).
		Where("state = ?", models.MonitorStatePending).Count(&summary.PendingCount).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.ComponentHealthState{}).
		Where("installed = ? AND health_state = ?", true, models.MonitorStateFiring).
		Count(&summary.ServiceFiringCount).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.ComponentHealthState{}).
		Where("installed = ? AND health_state = ?", true, models.MonitorStatePending).
		Count(&summary.ServicePendingCount).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.MonitorAlertEvent{}).
		Where("occurred_at >= ?", manager.now().UTC().Add(-24*time.Hour)).
		Count(&summary.Last24Hours).Error; err != nil {
		return nil, err
	}
	return summary, nil
}

func (manager *Manager) Cleanup() error {
	metricCutoff := manager.now().UTC().AddDate(0, 0, -manager.retentionDays)
	alertCutoff := manager.now().UTC().AddDate(0, 0, -manager.alertRetention)
	return manager.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("captured_at < ?", metricCutoff).Delete(&models.MetricSample{}).Error; err != nil {
			return err
		}
		var eventIDs []uint64
		if err := tx.Model(&models.MonitorAlertEvent{}).Where("occurred_at < ?", alertCutoff).
			Pluck("id", &eventIDs).Error; err != nil {
			return err
		}
		if len(eventIDs) > 0 {
			if err := tx.Where("event_id IN ?", eventIDs).Delete(&models.NotificationDelivery{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", eventIDs).Delete(&models.MonitorAlertEvent{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func validateRule(input RuleInput) error {
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > 120 {
		return errors.New("rule name must contain 1 to 120 characters")
	}
	switch input.Metric {
	case MetricCPU, MetricMemory, MetricDisk, MetricLoad1,
		MetricNetReceive, MetricNetSend, MetricDiskRead, MetricDiskWrite:
	default:
		return errors.New("unsupported monitor metric")
	}
	switch input.Operator {
	case "gt", "gte", "lt", "lte":
	default:
		return errors.New("operator must be gt, gte, lt, or lte")
	}
	if math.IsNaN(input.Threshold) || math.IsInf(input.Threshold, 0) ||
		math.IsNaN(input.RecoveryThreshold) || math.IsInf(input.RecoveryThreshold, 0) {
		return errors.New("thresholds must be finite")
	}
	if input.Metric == MetricCPU || input.Metric == MetricMemory || input.Metric == MetricDisk {
		if input.Threshold < 0 || input.Threshold > 100 ||
			input.RecoveryThreshold < 0 || input.RecoveryThreshold > 100 {
			return errors.New("percentage thresholds must be between 0 and 100")
		}
	}
	if (input.Operator == "gt" || input.Operator == "gte") &&
		input.RecoveryThreshold >= input.Threshold {
		return errors.New("recovery threshold must be below the firing threshold")
	}
	if (input.Operator == "lt" || input.Operator == "lte") &&
		input.RecoveryThreshold <= input.Threshold {
		return errors.New("recovery threshold must be above the firing threshold")
	}
	if input.ConsecutiveSamples < 1 || input.ConsecutiveSamples > 60 {
		return errors.New("consecutive samples must be between 1 and 60")
	}
	if input.CooldownMinutes < 1 || input.CooldownMinutes > 10080 {
		return errors.New("cooldown must be between 1 and 10080 minutes")
	}
	switch input.Severity {
	case "info", "warning", "critical":
	default:
		return errors.New("severity must be info, warning, or critical")
	}
	return nil
}

func metricValue(sample *models.MetricSample, metric string) float64 {
	switch metric {
	case MetricCPU:
		return sample.CPUPercent
	case MetricMemory:
		return sample.MemoryPercent
	case MetricDisk:
		return sample.DiskPercent
	case MetricLoad1:
		return sample.Load1
	case MetricNetReceive:
		return sample.NetworkReceiveBPS
	case MetricNetSend:
		return sample.NetworkSendBPS
	case MetricDiskRead:
		return sample.DiskReadBPS
	case MetricDiskWrite:
		return sample.DiskWriteBPS
	default:
		return 0
	}
}

func comparison(value float64, operator string, threshold float64) bool {
	switch operator {
	case "gt":
		return value > threshold
	case "gte":
		return value >= threshold
	case "lt":
		return value < threshold
	case "lte":
		return value <= threshold
	default:
		return false
	}
}

func recoveryComparison(value float64, operator string, threshold float64) bool {
	if operator == "gt" || operator == "gte" {
		return value <= threshold
	}
	return value >= threshold
}

func reminderDue(last *time.Time, cooldownMinutes int, now time.Time) bool {
	return last == nil || !last.Add(time.Duration(cooldownMinutes)*time.Minute).After(now)
}

func newAlertEvent(
	rule *models.MonitorRule,
	eventType string,
	value float64,
	started, occurred time.Time,
	resolved *time.Time,
) *models.MonitorAlertEvent {
	return &models.MonitorAlertEvent{
		RuleID: rule.ID, RuleName: rule.Name, Metric: rule.Metric,
		Severity: rule.Severity, EventType: eventType, Value: value,
		Threshold: rule.Threshold, StartedAt: started, OccurredAt: occurred,
		ResolvedAt: resolved,
		Message: fmt.Sprintf("%s: %s value %.2f (threshold %.2f)",
			rule.Name, eventType, value, rule.Threshold),
	}
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.Map(func(char rune) rune {
		if char < 32 || char == 127 {
			return -1
		}
		return char
	}, err.Error())
	if len(value) > 255 {
		value = value[:255]
	}
	return value
}
