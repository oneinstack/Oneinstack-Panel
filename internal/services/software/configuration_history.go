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

func ListConfigurationHistory(
	database *gorm.DB,
	component string,
	page int,
	pageSize int,
) (ConfigurationHistoryPage, error) {
	if database == nil {
		return ConfigurationHistoryPage{}, errors.New("database is not initialized")
	}
	definition, err := NormalizeServiceComponent(component)
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
	if err := query.Count(&total).Error; err != nil {
		return ConfigurationHistoryPage{}, fmt.Errorf("count configuration history: %w", err)
	}
	var rows []models.SoftwareConfigurationHistory
	if err := query.
		Order("created_at DESC").
		Limit(pageSize).
		Offset((page - 1) * pageSize).
		Find(&rows).Error; err != nil {
		return ConfigurationHistoryPage{}, fmt.Errorf("list configuration history: %w", err)
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
	definition, err := NormalizeServiceComponent(component)
	if err != nil {
		return ConfigurationHistoryEntry{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ConfigurationHistoryEntry{}, gorm.ErrRecordNotFound
	}
	var row models.SoftwareConfigurationHistory
	if err := database.
		Where("id = ? AND component = ?", id, definition.Component).
		First(&row).Error; err != nil {
		return ConfigurationHistoryEntry{}, err
	}
	return configurationHistoryEntry(row)
}

func configurationHistoryEntry(
	row models.SoftwareConfigurationHistory,
) (ConfigurationHistoryEntry, error) {
	before := make(map[string]string)
	if err := json.Unmarshal([]byte(row.BeforeJSON), &before); err != nil {
		return ConfigurationHistoryEntry{}, fmt.Errorf(
			"decode configuration history %s before values: %w",
			row.ID,
			err,
		)
	}
	after := make(map[string]string)
	if err := json.Unmarshal([]byte(row.AfterJSON), &after); err != nil {
		return ConfigurationHistoryEntry{}, fmt.Errorf(
			"decode configuration history %s after values: %w",
			row.ID,
			err,
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
