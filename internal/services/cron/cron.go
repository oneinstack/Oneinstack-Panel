package cron

import (
	"errors"
	"fmt"
	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services"
	"oneinstack/router/input"
	"os"
	"os/exec"
	"strings"

	"github.com/gin-gonic/gin"
)

func GetCronList(c *gin.Context, param *input.CronParam) (*services.PaginatedResult[models.Cron], error) {
	tx := app.DB().Model(&models.Cron{})
	if param.Name != "" {
		tx = tx.Where("name LIKE ?", "%"+param.Name+"%")
	}
	return services.Paginate[models.Cron](tx, &models.Cron{}, &input.Page{
		Page:     param.Page.Page,
		PageSize: param.Page.PageSize,
	})
}

func AddCron(c *gin.Context, param *input.AddCronParam) error {
	if param.CronType == "" {
		return errors.New("cron_type is required")
	}
	if param.CronType == "shell" && param.ShellContent == "" {
		return errors.New("shell_content is required")
	}
	if len(param.CronTimes) == 0 {
		return errors.New("cron_times is required")
	}
	if param.CronType == "shell" {
		err := UpdateCronTab(c, param)
		if err != nil {
			return err
		}
	}
	tx := app.DB().Create(&models.Cron{
		Name:         param.Name,
		ShellContent: param.ShellContent,
		Status:       param.Status,
		CronType:     param.CronType,
		CronTimes:    strings.Join(param.CronTimes, ","),
	})

	return tx.Error
}

func UpdateCron(c *gin.Context, param *input.AddCronParam) error {
	if param.CronType == "shell" {
		err := UpdateCronTab(c, param)
		if err != nil {
			return err
		}
	}
	tx := app.DB().Model(&models.Cron{}).Where("id = ?", param.ID).Updates(&models.Cron{
		Name:         param.Name,
		ShellContent: param.ShellContent,
		Status:       param.Status,
		CronType:     param.CronType,
		CronTimes:    strings.Join(param.CronTimes, ","),
	})
	return tx.Error
}

func UpdateCronTab(c *gin.Context, param *input.AddCronParam) error {
	// 构造crontab内容
	var cronEntries strings.Builder
	for _, t := range param.CronTimes {
		cronEntries.WriteString(fmt.Sprintf("%s %s\n", t, param.ShellContent))
	}

	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "cron")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// 写入crontab内容
	if _, err := tmpFile.WriteString(cronEntries.String()); err != nil {
		return fmt.Errorf("写入crontab内容失败: %v", err)
	}
	tmpFile.Close()

	// 使用crontab命令加载新配置
	cmd := exec.Command("crontab", tmpFile.Name())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入系统crontab失败: %v", err)
	}
	return nil
}

func DeleteCron(c *gin.Context, id int) error {
	// 获取要删除的任务
	var cron models.Cron
	if err := app.DB().First(&cron, id).Error; err != nil {
		return err
	}

	// 从数据库删除
	if err := app.DB().Delete(&cron).Error; err != nil {
		return err
	}

	// 更新系统crontab
	return reloadSystemCrontab()
}

func DisableCron(c *gin.Context, ids []int) error {
	// 更新状态为禁用
	if err := app.DB().Model(&models.Cron{}).Where("id IN ?", ids).Update("status", 0).Error; err != nil {
		return err
	}

	// 更新系统crontab
	return reloadSystemCrontab()
}

func EnableCron(c *gin.Context, ids []int) error {
	// 更新状态为启用
	if err := app.DB().Model(&models.Cron{}).Where("id IN ?", ids).Update("status", 1).Error; err != nil {
		return err
	}

	// 更新系统crontab
	return reloadSystemCrontab()
}

func reloadSystemCrontab() error {
	// 获取所有启用的shell类型任务
	var crons []models.Cron
	if err := app.DB().Where("status = 1 AND cron_type = 'shell'").Find(&crons).Error; err != nil {
		return err
	}

	// 生成新的crontab内容
	var cronEntries strings.Builder
	for _, cron := range crons {
		times := strings.Split(cron.CronTimes, ",")
		for _, t := range times {
			cronEntries.WriteString(fmt.Sprintf("%s %s\n", t, cron.ShellContent))
		}
	}

	// 创建临时文件
	tmpFile, err := os.CreateTemp("", "cron")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	// 写入内容
	if _, err := tmpFile.WriteString(cronEntries.String()); err != nil {
		return fmt.Errorf("写入crontab内容失败: %v", err)
	}
	tmpFile.Close()

	// 加载新配置
	cmd := exec.Command("crontab", tmpFile.Name())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写入系统crontab失败: %v", err)
	}
	return nil
}
