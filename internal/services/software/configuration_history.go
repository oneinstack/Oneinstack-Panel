package software

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

type ConfigurationHistoryEntry struct {
	ID              string            `json:"id"`
	TaskID          string            `json:"taskId"`
	Component       string            `json:"component"`
	SoftwareKey     string            `json:"softwareKey"`
	SoftwareVersion string            `json:"softwareVersion"`
	BaseRevision    string            `json:"baseRevision"`
	Before          map[string]string `json:"before"`
	After           map[string]string `json:"after"`
	Status          string            `json:"status"`
	RestoreFromID   string            `json:"restoreFromId,omitempty"`
	RequestedBy     int64             `json:"requestedBy"`
	FinishedAt      *time.Time        `json:"finishedAt,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
}

type ConfigurationHistoryPage struct {
	Items    []ConfigurationHistoryEntry `json:"items"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

const configurationHistoryReadAttempts = 4

type configurationHistoryReadError struct {
	code       string
	safeDetail string
	err        error
}

func (e *configurationHistoryReadError) Error() string {
	return e.err.Error()
}

func (e *configurationHistoryReadError) Unwrap() error {
	return e.err
}

func (e *configurationHistoryReadError) ErrorCode() string {
	return e.code
}

func (e *configurationHistoryReadError) SafeErrorDetail(error) string {
	return e.safeDetail
}

func ListConfigurationHistory(
	database *gorm.DB,
	component string,
	page int,
	pageSize int,
) (ConfigurationHistoryPage, error) {
	if database == nil {
		return ConfigurationHistoryPage{}, errors.New("database is not initialized")
	}
	definition, err := normalizeConfigurationHistoryComponent(database, component)
	if err != nil {
		return ConfigurationHistoryPage{}, err
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query := database.Model(&models.SoftwareConfigurationHistory{}).
		Where("component = ?", definition.Component)
	var total int64
	if err := retryConfigurationHistoryRead(func() error {
		total = 0
		return query.Count(&total).Error
	}); err != nil {
		// Configuration history was added after managed component installs
		// already existed. A legacy database without the table therefore has no
		// history to return; keep this read endpoint usable until the next Panel
		// restart runs the normal startup migration.
		if isConfigurationHistoryTableMissing(err) {
			return emptyConfigurationHistoryPage(page, pageSize), nil
		}
		return ConfigurationHistoryPage{}, configurationHistoryDatabaseError(
			fmt.Errorf("count configuration history: %w", err),
		)
	}
	var rows []models.SoftwareConfigurationHistory
	if err := retryConfigurationHistoryRead(func() error {
		rows = nil
		return query.
			Order("created_at DESC").
			Limit(pageSize).
			Offset((page - 1) * pageSize).
			Find(&rows).Error
	}); err != nil {
		return ConfigurationHistoryPage{}, configurationHistoryDatabaseError(
			fmt.Errorf("list configuration history: %w", err),
		)
	}
	items := make([]ConfigurationHistoryEntry, 0, len(rows))
	for i := range rows {
		entry, err := configurationHistoryEntry(rows[i])
		if err != nil {
			return ConfigurationHistoryPage{}, err
		}
		items = append(items, entry)
	}
	return ConfigurationHistoryPage{
		Items:    items,
		Total:    total,
		Page:     page,
		PageSize: pageSize,
	}, nil
}

func GetConfigurationHistory(
	database *gorm.DB,
	component string,
	id string,
) (ConfigurationHistoryEntry, error) {
	if database == nil {
		return ConfigurationHistoryEntry{}, errors.New("database is not initialized")
	}
	definition, err := normalizeConfigurationHistoryComponent(database, component)
	if err != nil {
		return ConfigurationHistoryEntry{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ConfigurationHistoryEntry{}, gorm.ErrRecordNotFound
	}
	var row models.SoftwareConfigurationHistory
	if err := retryConfigurationHistoryRead(func() error {
		row = models.SoftwareConfigurationHistory{}
		return database.
			Where("id = ? AND component = ?", id, definition.Component).
			First(&row).Error
	}); err != nil {
		if isConfigurationHistoryTableMissing(err) {
			return ConfigurationHistoryEntry{}, gorm.ErrRecordNotFound
		}
		return ConfigurationHistoryEntry{}, err
	}
	return configurationHistoryEntry(row)
}

func emptyConfigurationHistoryPage(page int, pageSize int) ConfigurationHistoryPage {
	return ConfigurationHistoryPage{
		Items:    make([]ConfigurationHistoryEntry, 0),
		Total:    0,
		Page:     page,
		PageSize: pageSize,
	}
}

func normalizeConfigurationHistoryComponent(
	database *gorm.DB,
	component string,
) (ComponentServiceDefinition, error) {
	definition, err := NormalizeServiceComponent(component)
	if err == nil {
		return definition, nil
	}
	return ResolveServiceComponent(database, component)
}

func retryConfigurationHistoryRead(read func() error) error {
	var err error
	for attempt := 0; attempt < configurationHistoryReadAttempts; attempt++ {
		err = read()
		if err == nil || !isConfigurationHistoryDatabaseBusy(err) {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * 15 * time.Millisecond)
	}
	return err
}

func isConfigurationHistoryTableMissing(err error) bool {
	if err == nil {
		return false
	}
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "no such table") ||
		(strings.Contains(detail, "doesn't exist") &&
			strings.Contains(detail, "software_configuration_history"))
}

func isConfigurationHistoryDatabaseBusy(err error) bool {
	if err == nil {
		return false
	}
	detail := strings.ToLower(err.Error())
	return strings.Contains(detail, "database is locked") ||
		strings.Contains(detail, "database is busy") ||
		strings.Contains(detail, "sqlite_busy") ||
		strings.Contains(detail, "database table is locked")
}

func configurationHistoryEntry(
	row models.SoftwareConfigurationHistory,
) (ConfigurationHistoryEntry, error) {
	before, err := decodeConfigurationHistoryValues(row.BeforeJSON)
	if err != nil {
		return ConfigurationHistoryEntry{}, configurationHistoryDataError(
			fmt.Errorf("decode configuration history %s before values: %w", row.ID, err),
		)
	}
	after, err := decodeConfigurationHistoryValues(row.AfterJSON)
	if err != nil {
		return ConfigurationHistoryEntry{}, configurationHistoryDataError(
			fmt.Errorf("decode configuration history %s after values: %w", row.ID, err),
		)
	}
	return ConfigurationHistoryEntry{
		ID:              row.ID,
		TaskID:          row.TaskID,
		Component:       row.Component,
		SoftwareKey:     row.SoftwareKey,
		SoftwareVersion: row.SoftwareVersion,
		BaseRevision:    row.BaseRevision,
		Before:          before,
		After:           after,
		Status:          row.Status,
		RestoreFromID:   row.RestoreFromID,
		RequestedBy:     row.RequestedBy,
		FinishedAt:      row.FinishedAt,
		CreatedAt:       row.CreatedAt,
	}, nil
}

func decodeConfigurationHistoryValues(raw string) (map[string]string, error) {
	values := make(map[string]string)
	if strings.TrimSpace(raw) == "" || strings.EqualFold(strings.TrimSpace(raw), "null") {
		return values, nil
	}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = make(map[string]string)
	}
	return values, nil
}

func configurationHistoryDatabaseError(err error) error {
	detail := strings.ToLower(err.Error())
	switch {
	case strings.Contains(detail, "no such table"),
		strings.Contains(detail, "no such column"),
		strings.Contains(detail, "has no column named"):
		return &configurationHistoryReadError{
			code:       "CONFIG_HISTORY_SCHEMA_MISSING",
			safeDetail: "The Panel configuration history schema is missing. Update and restart Panel so the database migration can complete.",
			err:        err,
		}
	case isConfigurationHistoryDatabaseBusy(err):
		return &configurationHistoryReadError{
			code:       "CONFIG_HISTORY_DATABASE_BUSY",
			safeDetail: "The Panel database is busy. Retry after the active write transaction has completed.",
			err:        err,
		}
	default:
		return &configurationHistoryReadError{
			code:       "CONFIG_HISTORY_READ_FAILED",
			safeDetail: "The Panel database could not read component configuration history.",
			err:        err,
		}
	}
}

func configurationHistoryDataError(err error) error {
	return &configurationHistoryReadError{
		code:       "CONFIG_HISTORY_DATA_INVALID",
		safeDetail: "A component configuration history record is invalid. Inspect the Panel service log for the affected record ID.",
		err:        err,
	}
}
