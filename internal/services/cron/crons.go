package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

const (
	defaultTaskTimeout = 30 * time.Minute
	maxTaskTimeout     = 24 * time.Hour
	maxExecutionOutput = 1 << 20
	maxHistoryPerJob   = 10000
)

var (
	taskCredentialPattern = regexp.MustCompile(`(?i)(authorization|cookie|password|passwd|pwd|token|secret)(["']?\s*[:=]\s*["']?)([^,"'\s;]+)`)
	taskBearerPattern     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
)

type activeExecution struct {
	executionID uint
	cancel      context.CancelFunc
	reason      string
}

type CronService struct {
	cron   *cron.Cron
	parser cron.Parser

	mu      sync.Mutex
	jobMap  map[uint][]cron.EntryID
	running map[uint]bool
	active  map[uint]*activeExecution

	retentionDays int
	now           func() time.Time

	executionWG sync.WaitGroup
	lifecycleMu sync.Mutex
	stopOnce    sync.Once
	stopping    bool
}

func NewCronService() *CronService {
	service, err := NewCronServiceWithOptions(30, "25 5 * * *")
	if err != nil {
		panic(err)
	}
	return service
}

func NewCronServiceWithOptions(
	retentionDays int,
	cleanupSchedule string,
) (*CronService, error) {
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("cron execution retention must be between 1 and 3650 days")
	}
	parser := cron.NewParser(
		cron.SecondOptional |
			cron.Minute |
			cron.Hour |
			cron.Dom |
			cron.Month |
			cron.Dow |
			cron.Descriptor,
	)
	scheduler := cron.New(
		cron.WithParser(parser),
		cron.WithChain(
			cron.Recover(cron.DefaultLogger),
		),
	)
	service := &CronService{
		cron: scheduler, parser: parser,
		jobMap:        make(map[uint][]cron.EntryID),
		running:       make(map[uint]bool),
		active:        make(map[uint]*activeExecution),
		retentionDays: retentionDays,
		now:           time.Now,
	}
	if _, err := scheduler.AddFunc(cleanupSchedule, func() {
		if _, cleanupErr := service.CleanupExpiredExecutions(); cleanupErr != nil {
			log.Printf("cleanup cron execution logs: %v", cleanupErr)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid cron cleanup schedule: %w", err)
	}
	if err := service.recoverInterruptedExecutions(); err != nil {
		return nil, fmt.Errorf("recover interrupted cron executions: %w", err)
	}
	service.loadJobsFromDB()
	service.cron.Start()
	return service, nil
}

func (cs *CronService) loadJobsFromDB() {
	var jobs []models.CronJob
	if err := app.DB().Where("enabled = ?", true).Find(&jobs).Error; err != nil {
		log.Printf("load cron jobs: %v", err)
		return
	}
	for i := range jobs {
		if err := cs.addToScheduler(&jobs[i]); err != nil {
			log.Printf("schedule cron job %d: %v", jobs[i].ID, err)
		}
	}
}

func (cs *CronService) validateJob(job *models.CronJob) error {
	job.Name = strings.TrimSpace(job.Name)
	job.Description = strings.TrimSpace(job.Description)
	job.Schedule = normalizeSchedules(job.Schedule)
	job.ConcurrencyPolicy = strings.ToLower(strings.TrimSpace(job.ConcurrencyPolicy))
	job.TaskType = strings.ToLower(strings.TrimSpace(job.TaskType))
	if job.TaskType == "" {
		job.TaskType = TaskTypeShell
	}
	if job.Name == "" || len(job.Name) > 128 {
		return errors.New("task name must contain 1 to 128 characters")
	}
	switch job.TaskType {
	case TaskTypeShell:
		job.Command = strings.TrimSpace(job.Command)
		job.TemplateID = ""
		job.TemplateParams = nil
		if job.Command == "" || len(job.Command) > 64*1024 ||
			strings.IndexByte(job.Command, 0) >= 0 {
			return errors.New("task command must contain 1 to 65536 valid characters")
		}
	case TaskTypeTemplate:
		templateID, parameters, err := normalizeTemplate(job.TemplateID, job.TemplateParams)
		if err != nil {
			return err
		}
		job.TemplateID = templateID
		job.TemplateParams = parameters
		job.Command = ""
	default:
		return errors.New("task type must be shell or template")
	}
	if job.Description != "" && len(job.Description) > 512 {
		return errors.New("task description cannot exceed 512 characters")
	}
	schedules := splitSchedules(job.Schedule)
	if len(schedules) == 0 || len(schedules) > 10 {
		return errors.New("task must contain between 1 and 10 schedules")
	}
	for _, schedule := range schedules {
		if _, err := cs.parser.Parse(schedule); err != nil {
			return fmt.Errorf("invalid cron expression %q: %w", schedule, err)
		}
	}
	if job.TimeoutSeconds == 0 {
		job.TimeoutSeconds = int(defaultTaskTimeout.Seconds())
	}
	if job.TimeoutSeconds < 1 || job.TimeoutSeconds > int(maxTaskTimeout.Seconds()) {
		return errors.New("task timeout must be between 1 and 86400 seconds")
	}
	if job.ConcurrencyPolicy == "" {
		job.ConcurrencyPolicy = "forbid"
	}
	if job.ConcurrencyPolicy != "forbid" {
		return errors.New("only the forbid concurrency policy is currently supported")
	}
	return nil
}

func normalizeSchedules(value string) string {
	parts := splitSchedules(value)
	return strings.Join(parts, ",")
}

func splitSchedules(value string) []string {
	raw := strings.Split(value, ",")
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, item := range raw {
		item = strings.Join(strings.Fields(item), " ")
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		result = append(result, item)
	}
	return result
}

func (cs *CronService) addToScheduler(job *models.CronJob) error {
	if err := cs.validateJob(job); err != nil {
		return err
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if cs.stopping {
		return errors.New("cron service is stopping")
	}
	if old := cs.jobMap[job.ID]; len(old) > 0 {
		for _, entryID := range old {
			cs.cron.Remove(entryID)
		}
		delete(cs.jobMap, job.ID)
	}
	added := make([]cron.EntryID, 0)
	for _, schedule := range splitSchedules(job.Schedule) {
		jobID := job.ID
		entryID, err := cs.cron.AddFunc(schedule, func() {
			cs.executeAsync(jobID, "scheduled")
		})
		if err != nil {
			for _, id := range added {
				cs.cron.Remove(id)
			}
			return err
		}
		added = append(added, entryID)
	}
	cs.jobMap[job.ID] = added
	return nil
}

func (cs *CronService) AddJob(job *models.CronJob) error {
	if err := cs.validateJob(job); err != nil {
		return err
	}
	if err := app.DB().Create(job).Error; err != nil {
		return err
	}
	if !job.Enabled {
		return nil
	}
	if err := cs.addToScheduler(job); err != nil {
		_ = app.DB().Delete(job).Error
		return err
	}
	return nil
}

func (cs *CronService) UpdateJob(id uint, changes *models.CronJob) error {
	var existing models.CronJob
	if err := app.DB().First(&existing, id).Error; err != nil {
		return err
	}
	existing.Name = changes.Name
	existing.Command = changes.Command
	existing.TaskType = changes.TaskType
	existing.TemplateID = changes.TemplateID
	existing.TemplateParams = changes.TemplateParams
	existing.Schedule = changes.Schedule
	existing.Description = changes.Description
	existing.Enabled = changes.Enabled
	existing.NotifyOnFailure = changes.NotifyOnFailure
	existing.TimeoutSeconds = changes.TimeoutSeconds
	existing.ConcurrencyPolicy = changes.ConcurrencyPolicy
	if err := cs.validateJob(&existing); err != nil {
		return err
	}
	existing.UpdatedAt = time.Now().UTC()
	// Saving the validated model lets GORM apply the JSON serializer for
	// TemplateParams. Passing map[string]string through Updates would reach the
	// SQLite driver as an unsupported native map value.
	if err := app.DB().Save(&existing).Error; err != nil {
		return err
	}
	cs.RemoveFromScheduler(id)
	if existing.Enabled {
		if err := cs.addToScheduler(&existing); err != nil {
			_ = app.DB().Model(&models.CronJob{}).Where("id = ?", id).
				Update("enabled", false).Error
			return err
		}
	}
	return nil
}

func (cs *CronService) SetEnabled(ids []int, enabled bool) error {
	if len(ids) == 0 {
		return errors.New("at least one task is required")
	}
	var jobs []models.CronJob
	if err := app.DB().Where("id IN ?", ids).Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) != len(uniqueIDs(ids)) {
		return gorm.ErrRecordNotFound
	}
	if enabled {
		for i := range jobs {
			if err := cs.validateJob(&jobs[i]); err != nil {
				return fmt.Errorf("task %d: %w", jobs[i].ID, err)
			}
		}
	}
	if err := app.DB().Model(&models.CronJob{}).
		Where("id IN ?", ids).
		Updates(map[string]any{"enabled": enabled, "updated_at": time.Now().UTC()}).Error; err != nil {
		return err
	}
	for i := range jobs {
		cs.RemoveFromScheduler(jobs[i].ID)
		if enabled {
			jobs[i].Enabled = true
			if err := cs.addToScheduler(&jobs[i]); err != nil {
				_ = app.DB().Model(&models.CronJob{}).Where("id IN ?", ids).
					Update("enabled", false).Error
				for j := range jobs {
					cs.RemoveFromScheduler(jobs[j].ID)
				}
				return err
			}
		}
	}
	return nil
}

func uniqueIDs(ids []int) map[int]struct{} {
	result := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			result[id] = struct{}{}
		}
	}
	return result
}

func (cs *CronService) DeleteJob(id uint) error {
	var job models.CronJob
	if err := app.DB().First(&job, id).Error; err != nil {
		return err
	}
	cs.mu.Lock()
	running := cs.running[id]
	cs.mu.Unlock()
	if running {
		return errors.New("cannot delete a running task")
	}
	if err := app.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("cron_job_id = ?", id).Delete(&models.JobExecution{}).Error; err != nil {
			return err
		}
		return tx.Delete(&job).Error
	}); err != nil {
		return err
	}
	cs.RemoveFromScheduler(id)
	return nil
}

func (cs *CronService) DeleteJobs(ids []int) error {
	unique := uniqueIDs(ids)
	if len(unique) == 0 {
		return errors.New("at least one task is required")
	}
	var jobs []models.CronJob
	if err := app.DB().Where("id IN ?", ids).Find(&jobs).Error; err != nil {
		return err
	}
	if len(jobs) != len(unique) {
		return gorm.ErrRecordNotFound
	}
	cs.mu.Lock()
	for i := range jobs {
		if cs.running[jobs[i].ID] {
			cs.mu.Unlock()
			return fmt.Errorf("cannot delete running task %d", jobs[i].ID)
		}
	}
	cs.mu.Unlock()
	if err := app.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("cron_job_id IN ?", ids).
			Delete(&models.JobExecution{}).Error; err != nil {
			return err
		}
		return tx.Where("id IN ?", ids).Delete(&models.CronJob{}).Error
	}); err != nil {
		return err
	}
	for i := range jobs {
		cs.RemoveFromScheduler(jobs[i].ID)
	}
	return nil
}

func (cs *CronService) RemoveFromScheduler(id uint) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for _, entryID := range cs.jobMap[id] {
		cs.cron.Remove(entryID)
	}
	delete(cs.jobMap, id)
}

func (cs *CronService) RunNow(id uint) (*models.JobExecution, error) {
	var job models.CronJob
	if err := app.DB().First(&job, id).Error; err != nil {
		return nil, err
	}
	execution, err := cs.reserveExecution(&job, "manual")
	if err != nil {
		return nil, err
	}
	if execution.Status == "skipped" {
		return execution, nil
	}
	cs.launchExecution(&job, execution)
	return execution, nil
}

func (cs *CronService) executeAsync(id uint, trigger string) {
	var job models.CronJob
	if err := app.DB().First(&job, id).Error; err != nil {
		log.Printf("load cron job %d: %v", id, err)
		return
	}
	execution, err := cs.reserveExecution(&job, trigger)
	if err != nil {
		log.Printf("reserve cron job %d: %v", id, err)
		return
	}
	if execution.Status == "skipped" {
		return
	}
	cs.launchExecution(&job, execution)
}

func (cs *CronService) launchExecution(job *models.CronJob, execution *models.JobExecution) {
	cs.lifecycleMu.Lock()
	timeout := time.Duration(job.TimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cs.mu.Lock()
	if cs.stopping {
		delete(cs.running, job.ID)
		cs.mu.Unlock()
		cs.lifecycleMu.Unlock()
		cancel()
		now := time.Now().UTC()
		execution.EndTime = now
		execution.DurationMs = now.Sub(execution.StartTime).Milliseconds()
		execution.Status = "canceled"
		execution.ErrorCode = "SERVICE_STOPPING"
		execution.Output = "计划任务服务正在停止，任务未执行"
		_ = cs.persistExecution(job.ID, execution)
		return
	}
	cs.active[job.ID] = &activeExecution{
		executionID: execution.ID, cancel: cancel, reason: "TASK_CANCELED",
	}
	cs.executionWG.Add(1)
	cs.mu.Unlock()
	go func() {
		defer cs.executionWG.Done()
		cs.runReserved(ctx, cancel, job, execution)
	}()
	cs.lifecycleMu.Unlock()
}

func (cs *CronService) reserveExecution(
	job *models.CronJob,
	trigger string,
) (*models.JobExecution, error) {
	cs.mu.Lock()
	if cs.stopping {
		cs.mu.Unlock()
		return nil, errors.New("cron service is stopping")
	}
	if cs.running[job.ID] {
		cs.mu.Unlock()
		execution := &models.JobExecution{
			CronJobID: job.ID, StartTime: time.Now().UTC(), EndTime: time.Now().UTC(),
			Status: "skipped", Trigger: trigger,
			Output:    "上一次执行尚未结束，已按 forbid 并发策略跳过",
			ErrorCode: "CONCURRENT_RUN_SKIPPED", ExitCode: -1,
		}
		if err := app.DB().Create(execution).Error; err != nil {
			return nil, err
		}
		return execution, nil
	}
	cs.running[job.ID] = true
	cs.mu.Unlock()

	execution := &models.JobExecution{
		CronJobID: job.ID, StartTime: time.Now().UTC(),
		Status: "running", Trigger: trigger, ExitCode: -1,
	}
	if err := app.DB().Create(execution).Error; err != nil {
		cs.mu.Lock()
		delete(cs.running, job.ID)
		cs.mu.Unlock()
		return nil, err
	}
	return execution, nil
}

func (cs *CronService) runReserved(
	ctx context.Context,
	cancel context.CancelFunc,
	job *models.CronJob,
	execution *models.JobExecution,
) {
	defer func() {
		cs.mu.Lock()
		delete(cs.running, job.ID)
		delete(cs.active, job.ID)
		cs.mu.Unlock()
	}()
	defer cancel()

	output := &boundedWriter{limit: maxExecutionOutput}
	command, commandErr := cs.executionCommand(job)
	if commandErr != nil {
		finished := time.Now().UTC()
		execution.EndTime = finished
		execution.DurationMs = finished.Sub(execution.StartTime).Milliseconds()
		execution.Status = "failed"
		execution.ErrorCode = "TEMPLATE_UNAVAILABLE"
		execution.ExitCode = -1
		execution.Output = commandErr.Error()
		if err := cs.persistExecution(job.ID, execution); err != nil {
			log.Printf("persist cron execution %d: %v", execution.ID, err)
		}
		cs.notifyFailure(job, execution)
		return
	}
	command.Dir = taskWorkingDirectory()
	command.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"LANG=C.UTF-8",
		fmt.Sprintf("ONEINSTACK_CRON_JOB_ID=%d", job.ID),
		fmt.Sprintf("ONEINSTACK_CRON_EXECUTION_ID=%d", execution.ID),
	}
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	runErr := runCommandWithContext(ctx, command)
	finished := time.Now().UTC()
	execution.EndTime = finished
	execution.DurationMs = finished.Sub(execution.StartTime).Milliseconds()
	execution.Output = sanitizeExecutionOutput(output.String())
	execution.OutputTruncated = output.Truncated()
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		execution.Status = "timeout"
		execution.ErrorCode = "TASK_TIMEOUT"
		execution.ExitCode = -1
	case errors.Is(ctx.Err(), context.Canceled):
		execution.Status = "canceled"
		cs.mu.Lock()
		if active := cs.active[job.ID]; active != nil && active.executionID == execution.ID {
			execution.ErrorCode = active.reason
		}
		cs.mu.Unlock()
		if execution.ErrorCode == "" {
			execution.ErrorCode = "TASK_CANCELED"
		}
		execution.ExitCode = -1
	case runErr != nil:
		execution.Status = "failed"
		execution.ErrorCode = "COMMAND_FAILED"
		var exitError *exec.ExitError
		if errors.As(runErr, &exitError) {
			execution.ExitCode = exitError.ExitCode()
		}
	default:
		execution.Status = "success"
		execution.ExitCode = 0
	}
	if execution.OutputTruncated {
		execution.Output += "\n[output truncated at 1 MiB]"
	}
	if err := cs.persistExecution(job.ID, execution); err != nil {
		log.Printf("persist cron execution %d: %v", execution.ID, err)
		return
	}
	cs.notifyFailure(job, execution)
}

func (cs *CronService) executionCommand(job *models.CronJob) (*exec.Cmd, error) {
	if job.TaskType == "" || job.TaskType == TaskTypeShell {
		return exec.Command("/bin/bash", "--noprofile", "--norc", "-c", job.Command), nil
	}
	if job.TaskType != TaskTypeTemplate {
		return nil, errors.New("unsupported task type")
	}
	executable, arguments, err := templateCommand(job.TemplateID, job.TemplateParams)
	if err != nil {
		return nil, err
	}
	return exec.Command(executable, arguments...), nil
}

func (cs *CronService) CancelExecution(executionID uint) (*models.JobExecution, error) {
	if executionID == 0 {
		return nil, errors.New("execution id is required")
	}
	var execution models.JobExecution
	if err := app.DB().First(&execution, executionID).Error; err != nil {
		return nil, err
	}
	if execution.Status != "running" {
		return nil, errors.New("execution is no longer running")
	}
	cs.mu.Lock()
	active := cs.active[execution.CronJobID]
	if active == nil || active.executionID != execution.ID {
		cs.mu.Unlock()
		return nil, errors.New("execution is not active in this panel process")
	}
	active.reason = "USER_CANCELED"
	active.cancel()
	cs.mu.Unlock()
	return &execution, nil
}

func (cs *CronService) RunningExecutions() ([]models.JobExecution, error) {
	var executions []models.JobExecution
	err := app.DB().Where("status = ?", "running").
		Order("start_time ASC").Find(&executions).Error
	return executions, err
}

func (cs *CronService) CleanupExpiredExecutions() (int64, error) {
	cutoff := cs.now().UTC().AddDate(0, 0, -cs.retentionDays)
	return cs.CleanupExecutionsBefore(cutoff)
}

func (cs *CronService) CleanupExecutionsBefore(cutoff time.Time) (int64, error) {
	if cutoff.IsZero() || cutoff.After(cs.now().UTC()) {
		return 0, errors.New("cleanup cutoff must not be in the future")
	}
	result := app.DB().Where("start_time < ? AND status <> ?", cutoff.UTC(), "running").
		Delete(&models.JobExecution{})
	return result.RowsAffected, result.Error
}

func (cs *CronService) recoverInterruptedExecutions() error {
	now := cs.now().UTC()
	return app.DB().Model(&models.JobExecution{}).
		Where("status = ?", "running").
		Updates(map[string]any{
			"status":      "canceled",
			"error_code":  "PANEL_RESTARTED",
			"end_time":    now,
			"duration_ms": 0,
			"exit_code":   -1,
			"output":      "面板进程重启，原执行已中断",
		}).Error
}

func (cs *CronService) notifyFailure(job *models.CronJob, execution *models.JobExecution) {
	if !job.NotifyOnFailure ||
		(execution.Status != "failed" && execution.Status != "timeout") {
		return
	}
	manager := monitoring.Default()
	if manager == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := manager.NotifyTaskFailure(ctx, job, execution); err != nil {
		log.Printf("notify cron execution %d failure: %v", execution.ID, err)
	}
}

func runCommandWithContext(ctx context.Context, command *exec.Cmd) error {
	if err := command.Start(); err != nil {
		return err
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		if command.Process != nil {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		}
		select {
		case <-wait:
		case <-time.After(3 * time.Second):
			if command.Process != nil {
				_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			}
			<-wait
		}
		return ctx.Err()
	}
}

func taskWorkingDirectory() string {
	path := filepath.Clean(app.GetBasePath())
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return string(filepath.Separator)
}

func (cs *CronService) persistExecution(jobID uint, execution *models.JobExecution) error {
	return app.DB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(execution).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.CronJob{}).Where("id = ?", jobID).
			Updates(map[string]any{
				"last_run_at": execution.EndTime,
				"updated_at":  time.Now().UTC(),
			}).Error; err != nil {
			return err
		}
		var expiredIDs []uint
		if err := tx.Model(&models.JobExecution{}).
			Where("cron_job_id = ?", jobID).
			Order("start_time DESC").
			Offset(maxHistoryPerJob).
			Pluck("id", &expiredIDs).Error; err != nil {
			return err
		}
		if len(expiredIDs) > 0 {
			if err := tx.Where("id IN ?", expiredIDs).
				Delete(&models.JobExecution{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (cs *CronService) Stop(ctx context.Context) error {
	var schedulerContext context.Context
	cs.stopOnce.Do(func() {
		cs.lifecycleMu.Lock()
		cs.mu.Lock()
		cs.stopping = true
		for _, active := range cs.active {
			active.reason = "SERVICE_STOPPING"
			active.cancel()
		}
		cs.mu.Unlock()
		schedulerContext = cs.cron.Stop()
		cs.lifecycleMu.Unlock()
	})
	if schedulerContext != nil {
		select {
		case <-schedulerContext.Done():
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	go func() {
		cs.executionWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type boundedWriter struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLength := len(data)
	remaining := w.limit - len(w.data)
	if remaining <= 0 {
		w.truncated = true
		return originalLength, nil
	}
	if len(data) > remaining {
		w.data = append(w.data, data[:remaining]...)
		w.truncated = true
		return originalLength, nil
	}
	w.data = append(w.data, data...)
	return originalLength, nil
}

func (w *boundedWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(append([]byte(nil), w.data...))
}

func (w *boundedWriter) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.truncated
}

func sanitizeExecutionOutput(output string) string {
	output = strings.ToValidUTF8(output, "\uFFFD")
	output = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\r' || character == '\t' {
			return character
		}
		if character < 32 || character == 127 {
			return -1
		}
		return character
	}, output)
	output = taskBearerPattern.ReplaceAllString(output, "Bearer [REDACTED]")
	return taskCredentialPattern.ReplaceAllString(output, "${1}${2}[REDACTED]")
}

var _ io.Writer = (*boundedWriter)(nil)
