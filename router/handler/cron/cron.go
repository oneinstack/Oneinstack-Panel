package cron

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/cron"
	"oneinstack/router/input"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const cronTaskTypeTemplate = "template"

var (
	cronService     *cron.CronService
	cronServiceErr  error
	cronServiceOnce sync.Once
)

// InitializeService starts the scheduler after the application database has
// been initialized. Keeping this out of package initialization makes imports
// safe for tests and read-only commands.
func InitializeService() error {
	cronServiceOnce.Do(func() {
		cronService, cronServiceErr = cron.NewCronServiceWithOptions(
			app.ONE_CONFIG.System.CronExecutionRetentionDays,
			app.ONE_CONFIG.System.CronExecutionCleanupSchedule,
		)
	})
	return cronServiceErr
}

func getCronService() (*cron.CronService, error) {
	if err := InitializeService(); err != nil {
		return nil, err
	}
	if cronService == nil {
		return nil, errors.New("cron service is unavailable")
	}
	return cronService, nil
}

func cronServiceOrUnavailable(c *gin.Context) (*cron.CronService, bool) {
	service, err := getCronService()
	if err != nil {
		core.HandleErrorWithStatus(c, http.StatusServiceUnavailable,
			core.NewErrorWithDetail(core.ErrTaskServiceUnavailable, "计划任务服务不可用，请稍后重试", "计划任务服务未初始化，无法执行当前操作；具体原因："+err.Error()))
		return nil, false
	}
	return service, true
}

func GetCronList(c *gin.Context) {
	var param input.CronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "计划任务查询参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	p, err := cron.GetCronList(c, &param)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "查询计划任务列表失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, p)
}

func GetCronLogList(c *gin.Context) {
	var param input.CronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "计划任务日志查询参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	p, err := cron.GetCronLogList(c, &param)
	if err != nil {
		appErr := core.WrapError(err, core.ErrInternalError, "查询计划任务日志失败")
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, p)
}

func AddCron(c *gin.Context) {
	var param input.AddCronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "创建计划任务参数格式不正确")
		core.HandleError(c, appErr)
		return
	}

	taskType := normalizeCronTaskType(param.TaskType, param.TemplateID)
	if taskType == cron.TaskTypeShell && !param.ConfirmUnsafeShell {
		core.HandleError(c, core.NewError(
			core.ErrBadRequest,
			"自定义 Shell 以面板权限执行，必须显式确认风险",
		))
		return
	}
	if taskType == cronTaskTypeTemplate && strings.TrimSpace(param.TemplateID) == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "安全模板任务必须选择模板"))
		return
	}
	job := &models.CronJob{
		Command:           param.Command,
		TaskType:          taskType,
		TemplateID:        param.TemplateID,
		TemplateParams:    param.TemplateParams,
		Schedule:          strings.Join(param.Schedule, ","),
		Description:       param.Description,
		Name:              param.Name,
		Enabled:           true,
		NotifyOnFailure:   param.NotifyOnFailure,
		TimeoutSeconds:    param.TimeoutSeconds,
		ConcurrencyPolicy: param.ConcurrencyPolicy,
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	if err := service.AddJob(job); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, cronAddErrorMessage(taskType))
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, job)
}

func UpdateCron(c *gin.Context) {
	var param input.AddCronParam
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "更新计划任务参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	taskType := normalizeCronTaskType(param.TaskType, param.TemplateID)
	if taskType == cron.TaskTypeShell && !param.ConfirmUnsafeShell {
		core.HandleError(c, core.NewError(
			core.ErrBadRequest,
			"自定义 Shell 以面板权限执行，必须显式确认风险",
		))
		return
	}
	if taskType == cronTaskTypeTemplate && strings.TrimSpace(param.TemplateID) == "" {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "安全模板任务必须选择模板"))
		return
	}
	updateData := &models.CronJob{
		Command:           param.Command,
		TaskType:          taskType,
		TemplateID:        param.TemplateID,
		TemplateParams:    param.TemplateParams,
		Schedule:          strings.Join(param.Schedule, ","),
		Description:       param.Description,
		Name:              param.Name,
		Enabled:           param.Enabled,
		NotifyOnFailure:   param.NotifyOnFailure,
		TimeoutSeconds:    param.TimeoutSeconds,
		ConcurrencyPolicy: param.ConcurrencyPolicy,
		UpdatedAt:         time.Now(),
	}

	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	if err := service.UpdateJob(uint(param.ID), updateData); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, cronUpdateErrorMessage(taskType))
		core.HandleError(c, appErr)
		return
	}
	core.HandleSuccess(c, nil)
}

func normalizeCronTaskType(rawTaskType, templateID string) string {
	taskType := strings.ToLower(strings.TrimSpace(rawTaskType))
	switch taskType {
	case "", cron.TaskTypeShell:
		if strings.TrimSpace(templateID) != "" {
			return cron.TaskTypeTemplate
		}
		return cron.TaskTypeShell
	case cron.TaskTypeTemplate, "safe", "safe_template", "security_template":
		return cron.TaskTypeTemplate
	default:
		return taskType
	}
}

func cronAddErrorMessage(taskType string) string {
	if taskType == cronTaskTypeTemplate {
		return "创建安全模板任务失败"
	}
	return "创建计划任务失败"
}

func cronUpdateErrorMessage(taskType string) string {
	if taskType == cronTaskTypeTemplate {
		return "更新安全模板任务失败"
	}
	return "更新计划任务失败"
}

func DeleteCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "删除计划任务参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	if err := service.DeleteJobs(param.IDs); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "删除计划任务失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func DisableCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "禁用计划任务参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	if err := service.SetEnabled(param.IDs, false); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "禁用计划任务失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func EnableCron(c *gin.Context) {
	var param input.CronIDs
	if err := c.ShouldBindJSON(&param); err != nil {
		appErr := core.WrapError(err, core.ErrBadRequest, "启用计划任务参数格式不正确")
		core.HandleError(c, appErr)
		return
	}
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	if err := service.SetEnabled(param.IDs, true); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "启用计划任务失败"))
		return
	}
	core.HandleSuccess(c, nil)
}

func RunCron(c *gin.Context) {
	var param input.RunCronParam
	if err := c.ShouldBindJSON(&param); err != nil || param.ID == 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "请选择要执行的计划任务"))
		return
	}
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	execution, err := service.RunNow(param.ID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "执行计划任务失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, execution))
}

func ListTemplates(c *gin.Context) {
	core.HandleSuccess(c, cron.Templates())
}

func ListRunningExecutions(c *gin.Context) {
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	executions, err := service.RunningExecutions()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "查询运行中任务失败"))
		return
	}
	core.HandleSuccess(c, executions)
}

func CancelExecution(c *gin.Context) {
	executionID, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || executionID == 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "执行记录 ID 必须是正整数"))
		return
	}
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	execution, err := service.CancelExecution(uint(executionID))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		core.HandleErrorWithStatus(c, http.StatusNotFound,
			core.NewError(core.ErrNotFound, "执行记录不存在"))
		return
	}
	if err != nil {
		core.HandleErrorWithStatus(c, http.StatusConflict,
			core.WrapError(err, core.ErrBadRequest, "取消执行失败"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, execution))
}

func CleanupCronLogs(c *gin.Context) {
	service, ok := cronServiceOrUnavailable(c)
	if !ok {
		return
	}
	deleted, err := service.CleanupExpiredExecutions()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "清理计划任务日志失败"))
		return
	}
	core.HandleSuccess(c, gin.H{"deleted": deleted})
}

func ExportCronLogs(c *gin.Context) {
	jobID, err := strconv.ParseUint(strings.TrimSpace(c.Param("id")), 10, 32)
	if err != nil || jobID == 0 {
		core.HandleError(c, core.NewError(core.ErrBadRequest, "计划任务 ID 必须是正整数"))
		return
	}
	param := input.CronParam{
		ID: int(jobID), Status: strings.TrimSpace(c.Query("status")),
		StartAt: strings.TrimSpace(c.Query("startAt")),
		EndAt:   strings.TrimSpace(c.Query("endAt")),
	}
	executions, err := cron.GetCronExecutionsForExport(&param)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "导出筛选参数无效"))
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf(
		`attachment; filename="cron-%d-executions.csv"`, jobID,
	))
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(c.Writer)
	_ = writer.Write([]string{
		"execution_id", "task_id", "status", "trigger", "start_time", "end_time",
		"duration_ms", "exit_code", "error_code", "output_truncated", "output",
	})
	for _, execution := range executions {
		_ = writer.Write([]string{
			strconv.FormatUint(uint64(execution.ID), 10),
			strconv.FormatUint(uint64(execution.CronJobID), 10),
			execution.Status, execution.Trigger,
			execution.StartTime.UTC().Format(time.RFC3339Nano),
			execution.EndTime.UTC().Format(time.RFC3339Nano),
			strconv.FormatInt(execution.DurationMs, 10),
			strconv.Itoa(execution.ExitCode), execution.ErrorCode,
			strconv.FormatBool(execution.OutputTruncated),
			csvSafe(execution.Output),
		})
	}
	writer.Flush()
}

func csvSafe(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}

func StopService(ctx context.Context) error {
	if cronService == nil {
		return cronServiceErr
	}
	return cronService.Stop(ctx)
}
