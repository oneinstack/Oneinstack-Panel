package cron

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

type CronService struct {
	cron    *cron.Cron
	mu      sync.Mutex
	jobMap  map[uint][]cron.EntryID // 修改为存储多个EntryID
	maxHist int
}

func NewCronService() *CronService {
	// 使用支持标准cron格式（5字段）的解析器
	c := cron.New(
		cron.WithParser(
			cron.NewParser(
				cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
			),
		),
	)

	cs := &CronService{
		cron:    c,
		jobMap:  make(map[uint][]cron.EntryID),
		maxHist: 10,
	}

	// 初始化时加载数据库中的任务
	cs.loadJobsFromDB()
	cs.cron.Start()
	return cs
}

// 从数据库加载已启用的任务
func (cs *CronService) loadJobsFromDB() {
	var jobs []models.CronJob
	if err := app.DB().Where("enabled = ?", true).Find(&jobs).Error; err != nil {
		log.Printf("Error loading cron jobs from DB: %v", err)
		return
	}

	for _, job := range jobs {
		cs.addToScheduler(&job)
	}
}

// 添加任务到调度器
func (cs *CronService) addToScheduler(job *models.CronJob) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	schedules := strings.Split(job.Schedule, ",")
	for _, schedule := range schedules {
		if _, err := cron.ParseStandard(strings.TrimSpace(schedule)); err != nil {
			log.Printf("invalid cron expression '%s': %v", schedule, err)
			continue
		}
		entryID, err := cs.cron.AddFunc(schedule, cs.createJobFunc(job))
		if err != nil {
			log.Printf("Error adding job %d with schedule %s: %v", job.ID, schedule, err)
			continue
		}
		cs.jobMap[job.ID] = append(cs.jobMap[job.ID], entryID)
	}
}

// 创建任务执行函数
func (cs *CronService) createJobFunc(job *models.CronJob) func() {
	return func() {
		fmt.Println("执行任务", job.ID)
		execution := models.JobExecution{
			CronJobID: job.ID,
			StartTime: time.Now(),
			Status:    "running",
		}

		// 创建执行记录
		if err := app.DB().Create(&execution).Error; err != nil {
			log.Printf("Error creating execution record: %v", err)
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		cmd := exec.CommandContext(ctx, "bash", "-c", job.Command)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		err := cmd.Run()
		execution.EndTime = time.Now()

		// 更新执行记录
		execution.Output = core.TruncateString(stdout.String()+"\n"+stderr.String(), 1000)
		if err != nil {
			execution.Status = "failed"
			if exitErr, ok := err.(*exec.ExitError); ok {
				execution.ExitCode = exitErr.ExitCode()
			} else {
				execution.ExitCode = -1
			}
		} else {
			execution.Status = "success"
			execution.ExitCode = 0
		}

		// 保存执行记录并维护历史数量
		app.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Save(&execution).Error; err != nil {
				return err
			}

			// 删除旧的历史记录
			var count int64
			tx.Model(&models.JobExecution{}).Where("cron_job_id = ?", job.ID).Count(&count)
			if count > int64(cs.maxHist) {
				tx.Exec(`
					DELETE FROM job_execution
					WHERE id IN (
						SELECT id FROM job_execution
						WHERE cron_job_id = ? 
						ORDER BY start_time ASC 
						LIMIT ?
					)`, job.ID, count-int64(cs.maxHist))
			}
			return nil
		})
	}
}

// 添加新任务
func (cs *CronService) AddJob(job *models.CronJob) error {
	return app.DB().Transaction(func(tx *gorm.DB) error {
		// 验证多个cron表达式
		schedules := strings.Split(job.Schedule, ",")
		for _, s := range schedules {
			if _, err := cron.ParseStandard(strings.TrimSpace(s)); err != nil {
				return fmt.Errorf("invalid cron expression '%s': %w", s, err)
			}
		}

		if err := tx.Create(job).Error; err != nil {
			return err
		}

		if job.Enabled {
			cs.addToScheduler(job)
		}
		return nil
	})
}

// 更新任务
func (cs *CronService) UpdateJob(id uint, job *models.CronJob) error {
	return app.DB().Transaction(func(tx *gorm.DB) error {
		var existing models.CronJob
		if err := tx.First(&existing, id).Error; err != nil {
			return err
		}

		// 先移除旧任务
		if existing.Enabled {
			cs.RemoveFromScheduler(existing.ID)
		}

		// 更新数据库
		if err := tx.Model(&existing).Updates(job).Error; err != nil {
			return err
		}

		// 添加新任务
		if job.Enabled {
			cs.addToScheduler(&existing)
		}
		return nil
	})
}
func (cs *CronService) DeleteJob(id uint) error {
	return app.DB().Transaction(func(tx *gorm.DB) error {
		var job models.CronJob
		if err := tx.First(&job, id).Error; err != nil {
			return err
		}
		cs.RemoveFromScheduler(job.ID)
		return tx.Delete(&job).Error
	})
}

// 从调度器移除任务
func (cs *CronService) RemoveFromScheduler(id uint) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if entryIDs, ok := cs.jobMap[id]; ok {
		for _, entryID := range entryIDs {
			cs.cron.Remove(entryID)
		}
		delete(cs.jobMap, id)
	}
}

// 其他方法（DeleteJob、EnableJob等）实现类似...
