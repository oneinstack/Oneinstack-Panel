package cron

import (
	"errors"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"strings"

	"github.com/gin-gonic/gin"
)

func GetCronList(c *gin.Context, param *input.CronParam) (*services.PaginatedResult[models.CronJob], error) {
	tx := app.DB().Model(&models.CronJob{})
	if param.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+param.Name+"%")
	}
	return services.Paginate[models.CronJob](tx, &models.CronJob{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func AddCron(c *gin.Context, param *input.AddCronParam) error {
	if param.Command == "" {
		return errors.New("command is required")
	}
	if len(param.Schedule) == 0 {
		return errors.New("schedule is required")
	}
	tx := app.DB().Create(&models.CronJob{
		Command:     param.Command,
		Schedule:    strings.Join(param.Schedule, ","),
		Description: param.Description,
		Enabled:     true,
	})

	return tx.Error
}

func UpdateCron(c *gin.Context, param *input.AddCronParam) error {
	tx := app.DB().Model(&models.CronJob{}).Where("id = ?", param.ID).Updates(&models.CronJob{
		Command:     param.Command,
		Schedule:    strings.Join(param.Schedule, ","),
		Description: param.Description,
		Enabled:     true,
	})
	return tx.Error
}

func DeleteCron(c *gin.Context, id int) error {
	// 获取要删除的任务
	var cron models.CronJob
	if err := app.DB().First(&cron, id).Error; err != nil {
		return err
	}

	// 从数据库删除
	if err := app.DB().Delete(&cron).Error; err != nil {
		return err
	}

	return nil
}

func DisableCron(c *gin.Context, ids []int) error {
	// 更新状态为禁用
	if err := app.DB().Model(&models.CronJob{}).Where("id IN ?", ids).Update("enabled", false).Error; err != nil {
		return err
	}
	return nil
}

func EnableCron(c *gin.Context, ids []int) error {
	// 更新状态为启用
	if err := app.DB().Model(&models.CronJob{}).Where("id IN ?", ids).Update("enabled", true).Error; err != nil {
		return err
	}
	return nil
}

func GetCronByIDs(c *gin.Context, ids []int) ([]*models.CronJob, error) {
	var crons []*models.CronJob
	if err := app.DB().Where("id IN ?", ids).Find(&crons).Error; err != nil {
		return nil, err
	}
	return crons, nil
}

func GetCronLogList(c *gin.Context, param *input.CronParam) (*services.PaginatedResult[models.JobExecution], error) {
	tx := app.DB().Model(&models.JobExecution{}).Where("cron_job_id = ?", param.ID).Order("start_time DESC")
	return services.Paginate[models.JobExecution](tx, &models.JobExecution{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}
