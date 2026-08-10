package software

import (
	"context"
	"net/http"
	"time"

	"oneinstack/core"
	softwareService "oneinstack/internal/services/software"

	"github.com/gin-gonic/gin"
)

func GetSoftwareCatalogStatus(c *gin.Context) {
	status, err := softwareService.GetCatalogStatus()
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取软件商城同步状态失败"))
		return
	}
	core.HandleSuccess(c, status)
}

func SyncSoftwareCatalog(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	status, err := softwareService.SyncCatalogNow(ctx)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "同步 Center 软件商城失败"))
		return
	}
	c.JSON(http.StatusOK, core.SuccessResponseForContext(c, status))
}
