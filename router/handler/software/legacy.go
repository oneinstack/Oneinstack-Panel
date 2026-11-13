package software

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/legacyoneinstack"
	"oneinstack/internal/services/softwaretask"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func ScanLegacyEnvironment(c *gin.Context) {
	report, err := legacyoneinstack.Scan(c.Request.Context(), "")
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrConfigError, "读取 OneinStack 环境失败"))
		return
	}
	core.HandleSuccess(c, report)
}

func ImportLegacyEnvironment(c *gin.Context) {
	actor, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "无法识别当前用户"))
		return
	}
	m, err := getTaskManager()
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "任务服务不可用"))
		return
	}
	task, err := m.SubmitLegacyImport(c.Request.Context(), "", actor)
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrConflict, "接管失败，请确认没有活动任务并重新扫描"))
		return
	}
	c.JSON(http.StatusAccepted, core.SuccessResponseForContext(c, gin.H{"task": task}))
}

// AutoImportLegacy is run before website lifecycle/renewal workers. It is
// idempotent and fails open for Panel availability; existing services are never
// stopped when discovery is incomplete or a custom layout cannot be imported.
func AutoImportLegacy(ctx context.Context, m *softwaretask.Manager) error {
	var admin models.User
	if err := app.DB().Where("is_admin = ?", true).Order("id ASC").First(&admin).Error; err != nil {
		return nil
	}
	task, err := m.SubmitLegacyImport(ctx, "", admin.ID)
	if err != nil || task == nil {
		return err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if models.IsSoftwareTaskTerminal(task.Status) {
			if task.Status != models.SoftwareTaskStatusSucceeded {
				return fmt.Errorf("OneinStack adoption task %s: %s", task.ID, task.ErrorCode)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			task, err = m.Get(task.ID)
			if err != nil {
				return err
			}
		}
	}
}
