package software

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"oneinstack/app"
	"oneinstack/internal/models"
	softwareService "oneinstack/internal/services/software"
	"strings"
)

// AutomationNginxMigration exposes the same confirmed, pinned task workflow to
// opt-in automation credentials; no shell command or filesystem path is accepted.
func AutomationNginxMigration(c *gin.Context) {
	if c.Request.Method == "GET" {
		plan, err := softwareService.PreviewNginxMigration(c.Request.Context(), c.Query("version"))
		if err != nil {
			automationError(c, 409, "NGINX_MIGRATION_UNSUPPORTED", err.Error())
			return
		}
		automationOK(c, 200, plan)
		return
	}
	var request struct {
		Confirm  bool   `json:"confirm"`
		Version  string `json:"version"`
		Revision string `json:"revision"`
		SHA256   string `json:"sha256"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || !request.Confirm || len(request.Revision) != 64 || len(request.SHA256) != 64 {
		automationError(c, 400, "NGINX_MIGRATION_CONFIRMATION_REQUIRED", "需要确认已预览的源版本与组件包摘要")
		return
	}
	plan, err := softwareService.PreviewNginxMigration(c.Request.Context(), request.Version)
	if err != nil {
		automationError(c, 409, "NGINX_MIGRATION_UNSUPPORTED", err.Error())
		return
	}
	if plan.Revision != request.Revision || plan.Package == nil || plan.Package.PackageSHA256 != request.SHA256 {
		automationError(c, 409, "CONFIG_REVISION_CONFLICT", "源安装或组件包已变化，请重新预览")
		return
	}
	actor, ok := c.Get("automationActor")
	if !ok {
		automationError(c, 403, "AUTOMATION_ACTOR_INVALID", "审计用户不可用")
		return
	}
	manager, err := DefaultTaskManager()
	if err != nil {
		automationError(c, 503, "AUTOMATION_HOST_UNAVAILABLE", "任务引擎不可用")
		return
	}
	task, err := manager.SubmitNginxMigration(plan.Revision, plan.Version, plan.Package, actor.(int64))
	if err != nil {
		automationError(c, 409, "NGINX_MIGRATION_SUBMIT_FAILED", err.Error())
		return
	}
	automationOK(c, 202, gin.H{"taskId": task.ID, "status": task.Status})
}

func AutomationNginxMigrationTask(c *gin.Context) {
	id := strings.TrimSpace(c.Param("taskID"))
	if _, err := uuid.Parse(id); err != nil {
		automationError(c, 400, "INVALID_TASK_ID", "任务标识无效")
		return
	}
	var task models.SoftwareTask
	if err := app.DB().Where("id = ? AND operation = ?", id, "nginx_migrate").First(&task).Error; err != nil {
		automationError(c, 404, "TASK_NOT_FOUND", "迁移任务不存在")
		return
	}
	automationOK(c, 200, task)
}
