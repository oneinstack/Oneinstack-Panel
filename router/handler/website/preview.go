package website

import (
	"fmt"
	"net/http"

	"oneinstack/core"

	"github.com/gin-gonic/gin"
)

// rejectDirectMutation keeps legacy routes available for discovery while
// preventing them from bypassing the shared preview/confirmation workflow.
func rejectDirectMutation(c *gin.Context, operation string) bool {
	core.HandleErrorWithStatus(c, http.StatusPreconditionRequired,
		core.NewError(core.ErrOperationNotConfirmed,
			fmt.Sprintf("网站操作 %s 必须先创建并确认操作预览", operation)))
	return false
}
