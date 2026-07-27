package audit

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type Filter struct {
	Page       int
	PageSize   int
	StartAt    *time.Time
	EndAt      *time.Time
	Username   string
	Outcome    string
	Method     string
	Action     string
	RemoteIP   string
	Query      string
	Sensitive  *bool
	EventType  string
	StatusCode int
}

type ListResult struct {
	Items    []models.AuditEvent `json:"items"`
	Total    int64               `json:"total"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
}

type Stats struct {
	Total          int64  `json:"total"`
	Success        int64  `json:"success"`
	Failure        int64  `json:"failure"`
	Sensitive      int64  `json:"sensitive"`
	Last24Hours    int64  `json:"last24Hours"`
	LatestSequence uint64 `json:"latestSequence"`
}

func (manager *Manager) List(filter Filter) (*ListResult, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	page, pageSize := normalizePagination(filter.Page, filter.PageSize)
	query := applyFilter(manager.db.Model(&models.AuditEvent{}), filter)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, fmt.Errorf("count audit events: %w", err)
	}
	var events []models.AuditEvent
	if err := query.Order("sequence DESC").
		Offset((page - 1) * pageSize).
		Limit(pageSize).
		Find(&events).Error; err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	return &ListResult{Items: events, Total: total, Page: page, PageSize: pageSize}, nil
}

func (manager *Manager) Export(filter Filter, limit int) ([]models.AuditEvent, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	if limit < 1 {
		limit = 10000
	}
	if limit > maxExportRowsCap {
		limit = maxExportRowsCap
	}
	var events []models.AuditEvent
	if err := applyFilter(manager.db.Model(&models.AuditEvent{}), filter).
		Order("sequence DESC").
		Limit(limit).
		Find(&events).Error; err != nil {
		return nil, fmt.Errorf("export audit events: %w", err)
	}
	return events, nil
}

func (manager *Manager) Get(id uint64) (*models.AuditEvent, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	var event models.AuditEvent
	if err := manager.db.First(&event, id).Error; err != nil {
		return nil, err
	}
	return &event, nil
}

func (manager *Manager) Stats() (*Stats, error) {
	if manager == nil || manager.db == nil {
		return nil, errors.New("audit manager is not configured")
	}
	stats := &Stats{}
	checks := []struct {
		target *int64
		query  *gorm.DB
	}{
		{&stats.Total, manager.db.Model(&models.AuditEvent{})},
		{&stats.Success, manager.db.Model(&models.AuditEvent{}).Where("outcome = ?", "success")},
		{&stats.Failure, manager.db.Model(&models.AuditEvent{}).Where("outcome = ?", "failure")},
		{&stats.Sensitive, manager.db.Model(&models.AuditEvent{}).Where("sensitive = ?", true)},
		{&stats.Last24Hours, manager.db.Model(&models.AuditEvent{}).
			Where("created_at >= ?", manager.now().UTC().Add(-24*time.Hour))},
	}
	for _, check := range checks {
		if err := check.query.Count(check.target).Error; err != nil {
			return nil, fmt.Errorf("count audit statistics: %w", err)
		}
	}
	var latest models.AuditEvent
	err := manager.db.Select("sequence").Order("sequence DESC").First(&latest).Error
	if err == nil {
		stats.LatestSequence = latest.Sequence
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("read latest audit sequence: %w", err)
	}
	return stats, nil
}

func normalizePagination(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func applyFilter(query *gorm.DB, filter Filter) *gorm.DB {
	if filter.StartAt != nil {
		query = query.Where("created_at >= ?", filter.StartAt.UTC())
	}
	if filter.EndAt != nil {
		query = query.Where("created_at <= ?", filter.EndAt.UTC())
	}
	if value := strings.TrimSpace(filter.Username); value != "" {
		query = query.Where("username LIKE ? ESCAPE '\\'", containsPattern(value))
	}
	if value := strings.TrimSpace(filter.Outcome); value != "" {
		query = query.Where("outcome = ?", strings.ToLower(value))
	}
	if value := strings.TrimSpace(filter.Method); value != "" {
		query = query.Where("method = ?", strings.ToUpper(value))
	}
	if value := strings.TrimSpace(filter.Action); value != "" {
		query = query.Where("action LIKE ? ESCAPE '\\'", containsPattern(value))
	}
	if value := strings.TrimSpace(filter.RemoteIP); value != "" {
		query = query.Where("remote_ip = ?", value)
	}
	if value := strings.TrimSpace(filter.EventType); value != "" {
		query = query.Where("event_type = ?", strings.ToLower(value))
	}
	if filter.StatusCode > 0 {
		query = query.Where("status = ?", filter.StatusCode)
	}
	if filter.Sensitive != nil {
		query = query.Where("sensitive = ?", *filter.Sensitive)
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		pattern := containsPattern(value)
		query = query.Where(
			"request_id LIKE ? ESCAPE '\\' OR username LIKE ? ESCAPE '\\' OR action LIKE ? ESCAPE '\\' OR path LIKE ? ESCAPE '\\' OR remote_ip LIKE ? ESCAPE '\\'",
			pattern, pattern, pattern, pattern, pattern,
		)
	}
	return query
}

func containsPattern(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_")
	return "%" + replacer.Replace(strings.TrimSpace(value)) + "%"
}
