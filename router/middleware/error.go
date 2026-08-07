package middleware

import (
	"oneinstack/core"

	"github.com/gin-gonic/gin"
)

func writeAPIError(c *gin.Context, status int, code core.ErrorCode, message, detail string) {
	core.HandleErrorWithStatus(c, status, core.NewErrorWithDetail(code, message, detail))
	c.Abort()
}
