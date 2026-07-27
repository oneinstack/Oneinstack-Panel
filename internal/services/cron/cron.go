package cron

import (
	"errors"
	"github.com/gin-gonic/gin"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"strings"
	"time"

	"gorm.io/gorm"
)

func GetCronList(c *gin.Context, param *input.CronParam) (*services.PaginatedResult[models.CronJob], error) {
	tx := app.DB().Model(&models.CronJob{}).Order("created_at DESC")
	if param.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+param.Name+"%")
	}
	return services.Paginate[models.CronJob](tx, &models.CronJob{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func GetCronLogList(c *gin.Context, param *input.CronParam) (*services.PaginatedResult[models.JobExecution], error) {
	tx, err := filteredExecutions(param, 0)
	if err != nil {
		return nil, err
	}
	return services.Paginate[models.JobExecution](tx, &models.JobExecution{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func GetCronExecutionsForExport(param *input.CronParam) ([]models.JobExecution, error) {
	tx, err := filteredExecutions(param, 500)
	if err != nil {
		return nil, err
	}
	var executions []models.JobExecution
	err = tx.Find(&executions).Error
	return executions, err
}

func filteredExecutions(param *input.CronParam, limit int) (*gorm.DB, error) {
	if param == nil || param.ID <= 0 {
		return nil, errors.New("task id is required")
	}
	tx := app.DB().Model(&models.JobExecution{}).
		Where("cron_job_id = ?", param.ID).
		Order("start_time DESC")
	status := strings.ToLower(strings.TrimSpace(param.Status))
	if status != "" {
		switch status {
		case "running", "success", "failed", "timeout", "canceled", "skipped":
		default:
			return nil, errors.New("invalid execution status")
		}
		tx = tx.Where("status = ?", status)
	}
	startAt, err := parseExecutionTime(param.StartAt)
	if err != nil {
		return nil, err
	}
	endAt, err := parseExecutionTime(param.EndAt)
	if err != nil {
		return nil, err
	}
	if !startAt.IsZero() {
		tx = tx.Where("start_time >= ?", startAt)
	}
	if !endAt.IsZero() {
		tx = tx.Where("start_time <= ?", endAt)
	}
	if !startAt.IsZero() && !endAt.IsZero() {
		if endAt.Before(startAt) || endAt.Sub(startAt) > 366*24*time.Hour {
			return nil, errors.New("execution time range must be between 0 and 366 days")
		}
	}
	if limit > 0 {
		tx = tx.Limit(limit)
	}
	return tx, nil
}

func parseExecutionTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, errors.New("execution time must use RFC3339")
	}
	return parsed.UTC(), nil
}
