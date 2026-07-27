package log

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"oneinstack/internal/models"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const (
	LevelDebug   = "debug"
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"

	defaultQueueSize      = 4096
	defaultSubscriberSize = 256
	maxMessageBytes       = 4096
)

var (
	credentialPattern = regexp.MustCompile(`(?i)(authorization|cookie|password|passwd|pwd|token|secret)(["']?\s*[:=]\s*["']?)([^,"'\s;]+)`)
	bearerPattern     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	sourcePattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
)

type EntryInput struct {
	OccurredAt time.Time
	Level      string
	Source     string
	Message    string
}

type QueryFilter struct {
	AfterID, BeforeID uint64
	Limit             int
	Level             string
	Source            string
	Query             string
	StartAt, EndAt    time.Time
}

type QueryResult struct {
	Items      []models.RuntimeLogEntry `json:"items"`
	NextCursor uint64                   `json:"nextCursor"`
	OldestID   uint64                   `json:"oldestId"`
	HasMore    bool                     `json:"hasMore"`
}

type SourceCount struct {
	Source string `json:"source"`
	Count  int64  `json:"count"`
}

type Stats struct {
	Total         int64         `json:"total"`
	Last24Hours   int64         `json:"last24Hours"`
	ErrorCount    int64         `json:"errorCount"`
	LatestID      uint64        `json:"latestId"`
	Dropped       uint64        `json:"dropped"`
	RetentionDays int           `json:"retentionDays"`
	Sources       []SourceCount `json:"sources"`
}

type queuedEntry struct {
	input EntryInput
}

type Manager struct {
	db            *gorm.DB
	retentionDays int
	scheduler     *cron.Cron
	now           func() time.Time
	queue         chan queuedEntry
	stopCh        chan struct{}
	doneCh        chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
	startErr  error
	started   atomic.Bool
	stopping  atomic.Bool
	dropped   atomic.Uint64
	acceptMu  sync.RWMutex
	pending   sync.WaitGroup
	workerWG  sync.WaitGroup

	subscriberMu sync.Mutex
	subscribers  map[uint64]chan models.RuntimeLogEntry
	nextSubID    uint64
}

var defaultRuntimeManager struct {
	sync.RWMutex
	value *Manager
}

func NewRuntimeManager(
	db *gorm.DB,
	retentionDays int,
	cleanupSchedule string,
) (*Manager, error) {
	if db == nil {
		return nil, errors.New("runtime log database is not configured")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("runtime log retention must be between 1 and 3650 days")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	manager := &Manager{
		db: db, retentionDays: retentionDays, scheduler: scheduler,
		now: time.Now, queue: make(chan queuedEntry, defaultQueueSize),
		stopCh: make(chan struct{}), doneCh: make(chan struct{}),
		subscribers: make(map[uint64]chan models.RuntimeLogEntry),
	}
	if _, err := scheduler.AddFunc(cleanupSchedule, func() {
		if _, cleanupErr := manager.Cleanup(); cleanupErr != nil {
			manager.Enqueue(LevelError, "runtime-log", "runtime log cleanup failed: "+cleanupErr.Error())
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid runtime log cleanup schedule: %w", err)
	}
	return manager, nil
}

func ConfigureRuntimeDefault(manager *Manager) {
	defaultRuntimeManager.Lock()
	defaultRuntimeManager.value = manager
	defaultRuntimeManager.Unlock()
}

func RuntimeDefault() *Manager {
	defaultRuntimeManager.RLock()
	defer defaultRuntimeManager.RUnlock()
	return defaultRuntimeManager.value
}

func ClearRuntimeDefault(manager *Manager) {
	defaultRuntimeManager.Lock()
	if defaultRuntimeManager.value == manager {
		defaultRuntimeManager.value = nil
	}
	defaultRuntimeManager.Unlock()
}

func (manager *Manager) Start() error {
	if manager == nil {
		return errors.New("runtime log manager is nil")
	}
	manager.startOnce.Do(func() {
		manager.workerWG.Add(1)
		go manager.worker()
		manager.started.Store(true)
		manager.scheduler.Start()
	})
	return manager.startErr
}

func (manager *Manager) Stop(ctx context.Context) error {
	if manager == nil || !manager.started.Load() {
		return nil
	}
	manager.stopOnce.Do(func() {
		manager.acceptMu.Lock()
		manager.stopping.Store(true)
		manager.acceptMu.Unlock()
		schedulerStopped := manager.scheduler.Stop()
		go func() {
			<-schedulerStopped.Done()
			manager.pending.Wait()
			close(manager.stopCh)
			manager.workerWG.Wait()
			manager.closeSubscribers()
			close(manager.doneCh)
		}()
	})
	select {
	case <-manager.doneCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Append persists a structured entry synchronously. Production log writers use
// Enqueue so HTTP requests and standard logging never wait on SQLite.
func (manager *Manager) Append(ctx context.Context, input EntryInput) (*models.RuntimeLogEntry, error) {
	entry := normalizedEntry(input, manager.now)
	if entry.Message == "" {
		return nil, errors.New("runtime log message is empty")
	}
	if err := manager.db.WithContext(ctx).Create(&entry).Error; err != nil {
		return nil, err
	}
	manager.publish(entry)
	return &entry, nil
}

func (manager *Manager) Enqueue(level, source, message string) {
	if manager == nil || !manager.started.Load() {
		return
	}
	input := EntryInput{
		OccurredAt: manager.now().UTC(),
		Level:      level, Source: source, Message: message,
	}
	manager.acceptMu.RLock()
	defer manager.acceptMu.RUnlock()
	if manager.stopping.Load() {
		manager.dropped.Add(1)
		return
	}
	manager.pending.Add(1)
	select {
	case manager.queue <- queuedEntry{input: input}:
	default:
		manager.pending.Done()
		manager.dropped.Add(1)
	}
}

type runtimeWriter struct {
	manager *Manager
	source  string
}

func (manager *Manager) Writer(source string) io.Writer {
	return &runtimeWriter{manager: manager, source: normalizeSource(source)}
}

func (writer *runtimeWriter) Write(payload []byte) (int, error) {
	for _, line := range strings.Split(string(payload), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		writer.manager.Enqueue(inferLevel(line), writer.source, line)
	}
	return len(payload), nil
}

func (manager *Manager) worker() {
	defer manager.workerWG.Done()
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	batch := make([]queuedEntry, 0, 100)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		entries := make([]models.RuntimeLogEntry, 0, len(batch))
		for _, queued := range batch {
			entry := normalizedEntry(queued.input, manager.now)
			if entry.Message != "" {
				entries = append(entries, entry)
			}
		}
		if len(entries) > 0 {
			if err := manager.db.Create(&entries).Error; err != nil {
				manager.dropped.Add(uint64(len(entries)))
			} else {
				for index := range entries {
					manager.publish(entries[index])
				}
			}
		}
		for range batch {
			manager.pending.Done()
		}
		batch = batch[:0]
	}
	for {
		select {
		case item := <-manager.queue:
			batch = append(batch, item)
			if len(batch) >= 100 {
				flush()
			}
		case <-timer.C:
			flush()
		case <-manager.stopCh:
			flush()
			return
		}
	}
}

func (manager *Manager) Subscribe(buffer int) (<-chan models.RuntimeLogEntry, func()) {
	if buffer < 1 {
		buffer = defaultSubscriberSize
	}
	if buffer > 4096 {
		buffer = 4096
	}
	manager.subscriberMu.Lock()
	manager.nextSubID++
	id := manager.nextSubID
	channel := make(chan models.RuntimeLogEntry, buffer)
	manager.subscribers[id] = channel
	manager.subscriberMu.Unlock()
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			manager.subscriberMu.Lock()
			if current, exists := manager.subscribers[id]; exists {
				delete(manager.subscribers, id)
				close(current)
			}
			manager.subscriberMu.Unlock()
		})
	}
	return channel, cancel
}

func (manager *Manager) publish(entry models.RuntimeLogEntry) {
	manager.subscriberMu.Lock()
	defer manager.subscriberMu.Unlock()
	for id, subscriber := range manager.subscribers {
		select {
		case subscriber <- entry:
		default:
			delete(manager.subscribers, id)
			close(subscriber)
		}
	}
}

func (manager *Manager) closeSubscribers() {
	manager.subscriberMu.Lock()
	defer manager.subscriberMu.Unlock()
	for id, subscriber := range manager.subscribers {
		delete(manager.subscribers, id)
		close(subscriber)
	}
}

func (manager *Manager) Query(filter QueryFilter) (*QueryResult, error) {
	if err := validateFilter(&filter); err != nil {
		return nil, err
	}
	query := manager.filteredQuery(filter)
	descending := filter.AfterID == 0
	if filter.AfterID > 0 {
		query = query.Where("id > ?", filter.AfterID).Order("id ASC")
	} else {
		if filter.BeforeID > 0 {
			query = query.Where("id < ?", filter.BeforeID)
		}
		query = query.Order("id DESC")
	}
	var entries []models.RuntimeLogEntry
	if err := query.Limit(filter.Limit + 1).Find(&entries).Error; err != nil {
		return nil, err
	}
	hasMore := len(entries) > filter.Limit
	if hasMore {
		entries = entries[:filter.Limit]
	}
	if descending {
		for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
			entries[left], entries[right] = entries[right], entries[left]
		}
	}
	result := &QueryResult{Items: entries, HasMore: hasMore}
	if len(entries) > 0 {
		result.OldestID = entries[0].ID
		result.NextCursor = entries[len(entries)-1].ID
	}
	return result, nil
}

func (manager *Manager) filteredQuery(filter QueryFilter) *gorm.DB {
	query := manager.db.Model(&models.RuntimeLogEntry{})
	if filter.Level != "" {
		query = query.Where("level = ?", filter.Level)
	}
	if filter.Source != "" {
		query = query.Where("source = ?", filter.Source)
	}
	if filter.Query != "" {
		query = query.Where("message LIKE ? ESCAPE '\\'", "%"+escapeLike(filter.Query)+"%")
	}
	if !filter.StartAt.IsZero() {
		query = query.Where("occurred_at >= ?", filter.StartAt.UTC())
	}
	if !filter.EndAt.IsZero() {
		query = query.Where("occurred_at <= ?", filter.EndAt.UTC())
	}
	return query
}

func (manager *Manager) Stats() (*Stats, error) {
	stats := &Stats{
		Dropped: manager.dropped.Load(), RetentionDays: manager.retentionDays,
	}
	if err := manager.db.Model(&models.RuntimeLogEntry{}).Count(&stats.Total).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.RuntimeLogEntry{}).
		Where("occurred_at >= ?", manager.now().UTC().Add(-24*time.Hour)).
		Count(&stats.Last24Hours).Error; err != nil {
		return nil, err
	}
	if err := manager.db.Model(&models.RuntimeLogEntry{}).
		Where("level = ?", LevelError).Count(&stats.ErrorCount).Error; err != nil {
		return nil, err
	}
	var latest models.RuntimeLogEntry
	if err := manager.db.Order("id DESC").First(&latest).Error; err == nil {
		stats.LatestID = latest.ID
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if err := manager.db.Model(&models.RuntimeLogEntry{}).
		Select("source, count(*) AS count").Group("source").Order("count DESC").
		Scan(&stats.Sources).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

func (manager *Manager) Cleanup() (int64, error) {
	cutoff := manager.now().UTC().AddDate(0, 0, -manager.retentionDays)
	result := manager.db.Where("occurred_at < ?", cutoff).Delete(&models.RuntimeLogEntry{})
	return result.RowsAffected, result.Error
}

func Matches(entry models.RuntimeLogEntry, filter QueryFilter) bool {
	level := normalizeLevel(filter.Level)
	if level != "" && entry.Level != level {
		return false
	}
	if filter.Source != "" && entry.Source != filter.Source {
		return false
	}
	if filter.Query != "" &&
		!strings.Contains(strings.ToLower(entry.Message), strings.ToLower(filter.Query)) {
		return false
	}
	if !filter.StartAt.IsZero() && entry.OccurredAt.Before(filter.StartAt) {
		return false
	}
	if !filter.EndAt.IsZero() && entry.OccurredAt.After(filter.EndAt) {
		return false
	}
	return true
}

// ValidateFilter normalizes and validates a runtime log query before it is
// executed or reused to filter live entries.
func ValidateFilter(filter *QueryFilter) error {
	if filter.AfterID > 0 && filter.BeforeID > 0 {
		return errors.New("afterId and beforeId cannot be used together")
	}
	if filter.Limit < 1 {
		filter.Limit = 200
	}
	if filter.Limit > 1000 {
		filter.Limit = 1000
	}
	if filter.Level != "" {
		filter.Level = normalizeLevel(filter.Level)
		if filter.Level == "" {
			return errors.New("invalid runtime log level")
		}
	}
	filter.Source = strings.TrimSpace(strings.ToLower(filter.Source))
	if filter.Source != "" && !sourcePattern.MatchString(filter.Source) {
		return errors.New("invalid runtime log source")
	}
	filter.Query = strings.TrimSpace(filter.Query)
	if len(filter.Query) > 200 {
		return errors.New("runtime log query is too long")
	}
	if !filter.StartAt.IsZero() && !filter.EndAt.IsZero() {
		if filter.EndAt.Before(filter.StartAt) ||
			filter.EndAt.Sub(filter.StartAt) > 31*24*time.Hour {
			return errors.New("runtime log query range must be between 0 and 31 days")
		}
	}
	return nil
}

func validateFilter(filter *QueryFilter) error {
	return ValidateFilter(filter)
}

func normalizedEntry(input EntryInput, now func() time.Time) models.RuntimeLogEntry {
	occurred := input.OccurredAt.UTC()
	if occurred.IsZero() {
		occurred = now().UTC()
	}
	level := normalizeLevel(input.Level)
	if level == "" {
		level = inferLevel(input.Message)
	}
	return models.RuntimeLogEntry{
		OccurredAt: occurred,
		Level:      level,
		Source:     normalizeSource(input.Source),
		Message:    sanitizeMessage(input.Message),
	}
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case LevelDebug:
		return LevelDebug
	case LevelInfo:
		return LevelInfo
	case "warn", LevelWarning:
		return LevelWarning
	case "err", LevelError, "fatal", "panic":
		return LevelError
	default:
		return ""
	}
}

func inferLevel(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "panic"), strings.Contains(lower, "fatal"),
		strings.Contains(lower, "error"), strings.Contains(lower, "failed"),
		strings.Contains(lower, "failure"):
		return LevelError
	case strings.Contains(lower, "warn"):
		return LevelWarning
	case strings.Contains(lower, "debug"):
		return LevelDebug
	default:
		return LevelInfo
	}
}

func normalizeSource(source string) string {
	source = strings.ToLower(strings.TrimSpace(source))
	if !sourcePattern.MatchString(source) {
		return "panel"
	}
	return source
}

func sanitizeMessage(message string) string {
	message = strings.Map(func(character rune) rune {
		if character == '\t' {
			return ' '
		}
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, strings.TrimSpace(message))
	message = bearerPattern.ReplaceAllString(message, "Bearer [REDACTED]")
	message = credentialPattern.ReplaceAllString(message, "${1}${2}[REDACTED]")
	if len(message) > maxMessageBytes {
		message = message[:maxMessageBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
	}
	return message
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
