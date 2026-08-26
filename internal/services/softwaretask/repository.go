package softwaretask

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type ListOptions struct {
	RequestedBy int64
	IncludeAll  bool
	ActiveOnly  bool
	Component   string
	Status      string
	Page        int
	PageSize    int
}

type TaskList struct {
	Data     []models.SoftwareTask `json:"data"`
	Total    int64                 `json:"total"`
	Page     int                   `json:"page"`
	PageSize int                   `json:"pageSize"`
}

type LogChunk struct {
	Content    string `json:"content"`
	NextCursor int64  `json:"nextCursor"`
	EOF        bool   `json:"eof"`
}

type TaskStatsOptions struct {
	RequestedBy int64
	IncludeAll  bool
	Days        int
}

type ComponentStats struct {
	Component              string  `json:"component"`
	Total                  int     `json:"total"`
	Succeeded              int     `json:"succeeded"`
	Failed                 int     `json:"failed"`
	Canceled               int     `json:"canceled"`
	Interrupted            int     `json:"interrupted"`
	SuccessRate            float64 `json:"successRate"`
	AverageDurationSeconds float64 `json:"averageDurationSeconds"`
}

type ErrorCodeStats struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

type TaskStats struct {
	Since                  time.Time        `json:"since"`
	Total                  int              `json:"total"`
	Active                 int              `json:"active"`
	Succeeded              int              `json:"succeeded"`
	Failed                 int              `json:"failed"`
	Canceled               int              `json:"canceled"`
	Interrupted            int              `json:"interrupted"`
	SuccessRate            float64          `json:"successRate"`
	AverageDurationSeconds float64          `json:"averageDurationSeconds"`
	Components             []ComponentStats `json:"components"`
	ErrorCodes             []ErrorCodeStats `json:"errorCodes"`
}

func (m *Manager) Get(taskID string) (*models.SoftwareTask, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	var task models.SoftwareTask
	if err := m.db.First(&task, "id = ?", taskID).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (m *Manager) List(options ListOptions) (*TaskList, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	if options.Page <= 0 {
		options.Page = 1
	}
	if options.PageSize <= 0 {
		options.PageSize = 20
	}
	if options.PageSize > 100 {
		options.PageSize = 100
	}
	query := m.db.Model(&models.SoftwareTask{})
	if !options.IncludeAll {
		query = query.Where("requested_by = ?", options.RequestedBy)
	}
	if options.ActiveOnly {
		query = query.Where("status IN ?", models.ActiveSoftwareTaskStatuses())
	}
	if value := strings.TrimSpace(options.Component); value != "" {
		query = query.Where("component = ?", value)
	}
	if value := strings.TrimSpace(options.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.SoftwareTask
	if err := query.
		Order("created_at DESC").
		Offset((options.Page - 1) * options.PageSize).
		Limit(options.PageSize).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return &TaskList{
		Data:     tasks,
		Total:    total,
		Page:     options.Page,
		PageSize: options.PageSize,
	}, nil
}

func (m *Manager) EventsAfter(taskID string, after int64, limit int) ([]models.SoftwareTaskEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var events []models.SoftwareTaskEvent
	err := m.db.
		Where("task_id = ? AND seq > ?", taskID, after).
		Order("seq ASC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

func (m *Manager) ReadLog(taskID string, cursor int64, limit int64) (*LogChunk, error) {
	if cursor < 0 {
		return nil, errors.New("log cursor cannot be negative")
	}
	if limit <= 0 {
		limit = 64 * 1024
	}
	if limit > 64*1024 {
		limit = 64 * 1024
	}
	var task models.SoftwareTask
	if err := m.db.Select("id", "status", "log_path").
		First(&task, "id = ?", taskID).Error; err != nil {
		return nil, err
	}
	if strings.TrimSpace(task.LogPath) == "" {
		return &LogChunk{
			NextCursor: cursor,
			EOF:        models.IsSoftwareTaskTerminal(task.Status),
		}, nil
	}
	file, _, err := m.openLogFile(&task)
	if errors.Is(err, os.ErrNotExist) {
		return &LogChunk{
			NextCursor: cursor,
			EOF:        models.IsSoftwareTaskTerminal(task.Status),
		}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open software task log: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat software task log: %w", err)
	}
	if cursor > info.Size() {
		return nil, errors.New("log cursor exceeds current log size")
	}
	buffer := make([]byte, limit)
	read, readErr := file.ReadAt(buffer, cursor)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("read software task log: %w", readErr)
	}
	next := cursor + int64(read)
	return &LogChunk{
		Content:    string(buffer[:read]),
		NextCursor: next,
		EOF:        next >= info.Size() && models.IsSoftwareTaskTerminal(task.Status),
	}, nil
}

// OpenLog returns a verified regular log file for an authenticated download
// handler. The caller owns the returned file.
func (m *Manager) OpenLog(taskID string) (*os.File, os.FileInfo, string, error) {
	var task models.SoftwareTask
	if err := m.db.Select("id", "log_path").First(&task, "id = ?", taskID).Error; err != nil {
		return nil, nil, "", err
	}
	if strings.TrimSpace(task.LogPath) == "" {
		return nil, nil, "", os.ErrNotExist
	}
	file, info, err := m.openLogFile(&task)
	if err != nil {
		return nil, nil, "", err
	}
	return file, info, "oneinstack-install-" + task.ID + ".log", nil
}

func (m *Manager) openLogFile(task *models.SoftwareTask) (*os.File, os.FileInfo, error) {
	path, err := m.safeLogPath(task.ID, task.LogPath)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("software task log is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !os.SameFile(info, openedInfo) {
		file.Close()
		return nil, nil, errors.New("software task log changed while opening")
	}
	return file, openedInfo, nil
}

func (m *Manager) safeLogPath(taskID, path string) (string, error) {
	expectedName := "task_" + taskID + ".log"
	if filepath.Base(path) != expectedName {
		return "", errors.New("software task log path does not match task")
	}
	absoluteDirectory, err := filepath.Abs(m.logDir)
	if err != nil {
		return "", fmt.Errorf("resolve software task log directory: %w", err)
	}
	absolutePath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("resolve software task log path: %w", err)
	}
	relative, err := filepath.Rel(absoluteDirectory, absolutePath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("software task log path escapes log directory")
	}
	if relative != expectedName {
		return "", errors.New("software task log must be stored directly in the log directory")
	}
	return absolutePath, nil
}

func (m *Manager) Stats(options TaskStatsOptions) (*TaskStats, error) {
	if err := m.Start(); err != nil {
		return nil, err
	}
	if options.Days <= 0 {
		options.Days = 30
	}
	if options.Days > 3650 {
		options.Days = 3650
	}
	since := time.Now().UTC().AddDate(0, 0, -options.Days)
	base := m.db.Model(&models.SoftwareTask{})
	if !options.IncludeAll {
		base = base.Where("requested_by = ?", options.RequestedBy)
	}

	var active int64
	if err := base.Where("status IN ?", models.ActiveSoftwareTaskStatuses()).Count(&active).Error; err != nil {
		return nil, err
	}
	var tasks []models.SoftwareTask
	history := m.db.Model(&models.SoftwareTask{}).
		Where("status IN ? AND COALESCE(finished_at, updated_at) >= ?", []string{
			models.SoftwareTaskStatusSucceeded,
			models.SoftwareTaskStatusFailed,
			models.SoftwareTaskStatusCanceled,
			models.SoftwareTaskStatusInterrupted,
		}, since)
	if !options.IncludeAll {
		history = history.Where("requested_by = ?", options.RequestedBy)
	}
	if err := history.Find(&tasks).Error; err != nil {
		return nil, err
	}

	result := &TaskStats{
		Since:      since,
		Total:      len(tasks) + int(active),
		Active:     int(active),
		Components: make([]ComponentStats, 0),
		ErrorCodes: make([]ErrorCodeStats, 0),
	}
	componentMap := make(map[string]*ComponentStats)
	componentDurationCount := make(map[string]int)
	errorMap := make(map[string]int)
	var durationTotal float64
	var durationCount int
	for i := range tasks {
		task := &tasks[i]
		component := componentMap[task.Component]
		if component == nil {
			component = &ComponentStats{Component: task.Component}
			componentMap[task.Component] = component
		}
		component.Total++
		switch task.Status {
		case models.SoftwareTaskStatusSucceeded:
			result.Succeeded++
			component.Succeeded++
		case models.SoftwareTaskStatusFailed:
			result.Failed++
			component.Failed++
		case models.SoftwareTaskStatusCanceled:
			result.Canceled++
			component.Canceled++
		case models.SoftwareTaskStatusInterrupted:
			result.Interrupted++
			component.Interrupted++
		}
		if task.StartedAt != nil && task.FinishedAt != nil && !task.FinishedAt.Before(*task.StartedAt) {
			duration := task.FinishedAt.Sub(*task.StartedAt).Seconds()
			durationTotal += duration
			durationCount++
			component.AverageDurationSeconds += duration
			componentDurationCount[task.Component]++
		}
		if code := strings.TrimSpace(task.ErrorCode); code != "" {
			errorMap[code]++
		}
	}
	terminal := len(tasks)
	if terminal > 0 {
		result.SuccessRate = roundStats(float64(result.Succeeded) * 100 / float64(terminal))
	}
	if durationCount > 0 {
		result.AverageDurationSeconds = roundStats(durationTotal / float64(durationCount))
	}
	for _, component := range componentMap {
		if component.Total > 0 {
			component.SuccessRate = roundStats(float64(component.Succeeded) * 100 / float64(component.Total))
		}
		if componentDurationCount[component.Component] > 0 {
			component.AverageDurationSeconds = roundStats(component.AverageDurationSeconds / float64(componentDurationCount[component.Component]))
		}
		result.Components = append(result.Components, *component)
	}
	sort.Slice(result.Components, func(i, j int) bool {
		return result.Components[i].Component < result.Components[j].Component
	})
	for code, count := range errorMap {
		result.ErrorCodes = append(result.ErrorCodes, ErrorCodeStats{Code: code, Count: count})
	}
	sort.Slice(result.ErrorCodes, func(i, j int) bool {
		if result.ErrorCodes[i].Count == result.ErrorCodes[j].Count {
			return result.ErrorCodes[i].Code < result.ErrorCodes[j].Code
		}
		return result.ErrorCodes[i].Count > result.ErrorCodes[j].Count
	})
	return result, nil
}

func roundStats(value float64) float64 {
	return math.Round(value*100) / 100
}

func (m *Manager) Cancel(taskID string) (*models.SoftwareTask, error) {
	task, err := m.Get(taskID)
	if err != nil {
		return nil, err
	}
	if models.IsSoftwareTaskTerminal(task.Status) {
		return nil, fmt.Errorf("software task %s is already finished", taskID)
	}
	operationName := operationLabel(task.Operation)

	reporter := newReporter(m, taskID)
	reporter.mu.Lock()
	err = reporter.publishLocked(taskUpdate{
		status:          models.SoftwareTaskStatusCanceling,
		phase:           models.SoftwareTaskStatusCanceling,
		message:         "正在取消" + operationName + "任务",
		cancelRequested: boolPointer(true),
	}, eventData{
		eventType: "phase",
		level:     "warning",
		code:      "cancel_requested",
		message:   "已收到取消请求",
	})
	reporter.mu.Unlock()
	if err != nil {
		return nil, err
	}

	m.cancelMu.Lock()
	cancel, running := m.cancels[taskID]
	m.cancelMu.Unlock()
	if running {
		cancel()
	} else {
		if err := reporter.finish(
			models.SoftwareTaskStatusCanceled,
			"ACTION_CANCELED",
			"排队中的"+operationName+"任务已取消",
		); err != nil {
			return nil, err
		}
	}
	return m.Get(taskID)
}

func (m *Manager) Subscribe(taskID string) (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	m.subscribeMu.Lock()
	if m.subscribers[taskID] == nil {
		m.subscribers[taskID] = make(map[chan struct{}]struct{})
	}
	m.subscribers[taskID][channel] = struct{}{}
	m.subscribeMu.Unlock()

	return channel, func() {
		m.subscribeMu.Lock()
		delete(m.subscribers[taskID], channel)
		if len(m.subscribers[taskID]) == 0 {
			delete(m.subscribers, taskID)
		}
		m.subscribeMu.Unlock()
	}
}

func (m *Manager) notify(taskID string) {
	m.subscribeMu.Lock()
	defer m.subscribeMu.Unlock()
	for channel := range m.subscribers[taskID] {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

func boolPointer(value bool) *bool {
	result := value
	return &result
}

// Touch updates a running task heartbeat without adding an event. It is kept
// separate from progress updates so long-running compiles do not look stale.
func (m *Manager) Touch(taskID string) {
	now := time.Now()
	_ = m.db.Model(&models.SoftwareTask{}).
		Where("id = ? AND status IN ?", taskID, models.ActiveSoftwareTaskStatuses()).
		Updates(map[string]any{
			"heartbeat_at": now,
			"updated_at":   now,
		}).Error
}

func IsNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}
