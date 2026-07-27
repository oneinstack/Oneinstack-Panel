package cron

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
)

func TestCronEnableDisableUpdatesDatabaseAndScheduler(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "daily task", Command: "true", Schedule: "0 0 1 1 *",
		Enabled: true, TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	if len(service.jobMap[job.ID]) != 1 {
		t.Fatalf("scheduler entries = %d, want 1", len(service.jobMap[job.ID]))
	}
	if err := service.SetEnabled([]int{int(job.ID)}, false); err != nil {
		t.Fatal(err)
	}
	var stored models.CronJob
	if err := app.DB().First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enabled {
		t.Fatal("disabled task remained enabled in database")
	}
	if len(service.jobMap[job.ID]) != 0 {
		t.Fatal("disabled task remained in scheduler")
	}
	if err := service.SetEnabled([]int{int(job.ID)}, true); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !stored.Enabled || len(service.jobMap[job.ID]) != 1 {
		t.Fatal("enabled task was not persisted and scheduled")
	}
}

func TestCronManualRunUsesSanitizedEnvironmentAndPersistsOutput(t *testing.T) {
	service := prepareCronServiceTest(t)
	t.Setenv("JWT_SECRET_KEY", "must-not-reach-task")
	job := &models.CronJob{
		Name:     "environment test",
		Command:  `printf '%s|%s' "$ONEINSTACK_CRON_JOB_ID" "${JWT_SECRET_KEY-unset}"`,
		Schedule: "0 0 1 1 *", Enabled: false,
		TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	execution, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution = waitForCronExecution(t, execution.ID)
	if execution.Status != "success" || execution.ExitCode != 0 {
		t.Fatalf("unexpected execution: %+v", execution)
	}
	want := strings.Join([]string{strconv.FormatUint(uint64(job.ID), 10), "unset"}, "|")
	if execution.Output != want {
		t.Fatalf("output = %q, want %q", execution.Output, want)
	}
	if strings.Contains(execution.Output, "must-not-reach-task") {
		t.Fatal("Panel secret leaked into cron environment")
	}
}

func TestCronExecutionOutputRedactsCredentials(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name:     "redaction test",
		Command:  `printf '%s' 'password=plain-secret Authorization: Bearer abc.def.ghi'`,
		Schedule: "0 0 1 1 *", Enabled: false,
		TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	execution, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution = waitForCronExecution(t, execution.ID)
	if strings.Contains(execution.Output, "plain-secret") ||
		strings.Contains(execution.Output, "abc.def.ghi") ||
		!strings.Contains(execution.Output, "[REDACTED]") {
		t.Fatalf("execution output was not redacted: %q", execution.Output)
	}
}

func TestCronForbidPolicySkipsOverlappingRun(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "overlap test", Command: "sleep 1", Schedule: "0 0 1 1 *",
		Enabled: false, TimeoutSeconds: 10, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	first, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "skipped" ||
		second.ErrorCode != "CONCURRENT_RUN_SKIPPED" {
		t.Fatalf("overlapping run was not skipped: %+v", second)
	}
	if completed := waitForCronExecution(t, first.ID); completed.Status != "success" {
		t.Fatalf("first execution did not succeed: %+v", completed)
	}
}

func TestCronTimeoutTerminatesCommand(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "timeout test", Command: "sleep 10", Schedule: "0 0 1 1 *",
		Enabled: false, TimeoutSeconds: 1, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	execution, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution = waitForCronExecution(t, execution.ID)
	if execution.Status != "timeout" || execution.ErrorCode != "TASK_TIMEOUT" {
		t.Fatalf("unexpected timeout execution: %+v", execution)
	}
	if execution.DurationMs > 5000 {
		t.Fatalf("timed out process took too long: %d ms", execution.DurationMs)
	}
}

func TestCronSafeTemplateExecutesWithoutShell(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "disk report", TaskType: TaskTypeTemplate,
		TemplateID: "disk-usage-report", TemplateParams: map[string]string{},
		Schedule: "0 0 1 1 *", Enabled: false,
		TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	if job.Command != "" {
		t.Fatalf("template persisted a shell command: %q", job.Command)
	}
	execution, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	execution = waitForCronExecution(t, execution.ID)
	if execution.Status != "success" || !strings.Contains(execution.Output, "Filesystem") {
		t.Fatalf("unexpected template execution: %+v", execution)
	}
}

func TestCronTemplateRejectsInjectionAndUnknownParameters(t *testing.T) {
	service := prepareCronServiceTest(t)
	for name, parameters := range map[string]map[string]string{
		"command injection": {"service": "nginx; id"},
		"unknown field":     {"service": "nginx", "command": "id"},
	} {
		t.Run(name, func(t *testing.T) {
			job := &models.CronJob{
				Name: "invalid template", TaskType: TaskTypeTemplate,
				TemplateID: "service-status", TemplateParams: parameters,
				Schedule: "0 0 1 1 *", TimeoutSeconds: 30,
				ConcurrencyPolicy: "forbid",
			}
			if err := service.AddJob(job); err == nil {
				t.Fatal("unsafe template parameters were accepted")
			}
		})
	}
}

func TestCronTemplateUpdatePersistsStructuredParameters(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "disk report", TaskType: TaskTypeTemplate,
		TemplateID: "disk-usage-report", Schedule: "0 0 1 1 *",
		TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	changes := &models.CronJob{
		Name: "nginx status", TaskType: TaskTypeTemplate,
		TemplateID:     "service-status",
		TemplateParams: map[string]string{"service": "nginx"},
		Schedule:       "5 0 * * *", TimeoutSeconds: 60,
		ConcurrencyPolicy: "forbid", NotifyOnFailure: true,
	}
	if err := service.UpdateJob(job.ID, changes); err != nil {
		t.Fatal(err)
	}
	var stored models.CronJob
	if err := app.DB().First(&stored, job.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Command != "" || stored.TemplateID != "service-status" ||
		stored.TemplateParams["service"] != "nginx" || !stored.NotifyOnFailure {
		t.Fatalf("unexpected stored template task: %+v", stored)
	}
}

func TestCronExecutionCanBeCanceledByID(t *testing.T) {
	service := prepareCronServiceTest(t)
	job := &models.CronJob{
		Name: "cancel test", Command: "sleep 20", Schedule: "0 0 1 1 *",
		Enabled: false, TimeoutSeconds: 30, ConcurrencyPolicy: "forbid",
	}
	if err := service.AddJob(job); err != nil {
		t.Fatal(err)
	}
	execution, err := service.RunNow(job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CancelExecution(execution.ID); err != nil {
		t.Fatal(err)
	}
	completed := waitForCronExecution(t, execution.ID)
	if completed.Status != "canceled" || completed.ErrorCode != "USER_CANCELED" {
		t.Fatalf("unexpected canceled execution: %+v", completed)
	}
	if _, err := service.CancelExecution(execution.ID); err == nil {
		t.Fatal("completed execution was canceled twice")
	}
}

func TestCronServiceRecoversInterruptedExecution(t *testing.T) {
	service := prepareCronServiceTest(t)
	stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := service.Stop(stopContext); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	execution := models.JobExecution{
		CronJobID: 99, StartTime: time.Now().UTC().Add(-time.Minute),
		Status: "running", ExitCode: -1,
	}
	if err := app.DB().Create(&execution).Error; err != nil {
		t.Fatal(err)
	}
	recovered, err := NewCronServiceWithOptions(30, "25 5 * * *")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = recovered.Stop(ctx)
	})
	var stored models.JobExecution
	if err := app.DB().First(&stored, execution.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != "canceled" || stored.ErrorCode != "PANEL_RESTARTED" {
		t.Fatalf("interrupted execution was not recovered: %+v", stored)
	}
}

func TestCronExecutionRetentionKeepsRecentAndRunningRecords(t *testing.T) {
	service := prepareCronServiceTest(t)
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	records := []models.JobExecution{
		{CronJobID: 1, StartTime: now.AddDate(0, 0, -31), EndTime: now.AddDate(0, 0, -31), Status: "success"},
		{CronJobID: 1, StartTime: now.Add(-time.Hour), EndTime: now.Add(-time.Hour), Status: "failed"},
		{CronJobID: 1, StartTime: now.AddDate(0, 0, -31), Status: "running"},
	}
	if err := app.DB().Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := service.CleanupExpiredExecutions()
	if err != nil || deleted != 1 {
		t.Fatalf("cleanup deleted=%d err=%v", deleted, err)
	}
	var remaining int64
	if err := app.DB().Model(&models.JobExecution{}).Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("remaining executions=%d, want 2", remaining)
	}
}

func TestBoundedWriterTruncatesWithoutShortWrite(t *testing.T) {
	writer := &boundedWriter{limit: 4}
	written, err := writer.Write([]byte("123456"))
	if err != nil {
		t.Fatal(err)
	}
	if written != 6 || writer.String() != "1234" || !writer.Truncated() {
		t.Fatalf("unexpected bounded write: written=%d data=%q truncated=%v",
			written, writer.String(), writer.Truncated())
	}
}

func prepareCronServiceTest(t *testing.T) *CronService {
	t.Helper()
	originalBasePath := app.BASE_PATH
	root := t.TempDir()
	app.BASE_PATH = filepath.Clean(root) + string(os.PathSeparator)
	t.Cleanup(func() { app.BASE_PATH = originalBasePath })
	if err := app.InitDB(filepath.Join(root, "cron.db")); err != nil {
		t.Fatal(err)
	}
	service := NewCronService()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Errorf("stop cron service: %v", err)
		}
	})
	return service
}

func waitForCronExecution(t *testing.T, executionID uint) *models.JobExecution {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var execution models.JobExecution
		if err := app.DB().First(&execution, executionID).Error; err != nil {
			t.Fatal(err)
		}
		if execution.Status != "running" {
			return &execution
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("execution %d did not finish", executionID)
	return nil
}
