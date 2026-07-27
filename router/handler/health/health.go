package health

import (
	"context"
	"net/http"
	"time"

	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/buildinfo"
	"oneinstack/webui"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type probeResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Live reports whether the HTTP process can serve requests. It deliberately
// avoids dependencies so process supervisors can distinguish a dead process
// from a temporarily unavailable database.
func Live(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, probeResponse{Status: "ok"})
}

// Ready reports whether the database and embedded frontend required for a
// usable panel are available.
func Ready(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	ready, checks := checkReadiness(c.Request.Context(), app.DB(), webui.ReadFile)
	status := http.StatusOK
	result := "ok"
	if !ready {
		status = http.StatusServiceUnavailable
		result = "unavailable"
	}
	c.JSON(status, probeResponse{Status: result, Checks: checks})
}

// Version returns build metadata only to an authenticated panel user.
func Version(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	core.HandleSuccess(c, buildinfo.Current())
}

func checkReadiness(
	parent context.Context,
	db *gorm.DB,
	readFile func(string) ([]byte, error),
) (bool, map[string]string) {
	checks := map[string]string{
		"database": "unavailable",
		"webui":    "unavailable",
	}

	if db != nil {
		sqlDB, err := db.DB()
		if err == nil {
			ctx, cancel := context.WithTimeout(parent, 2*time.Second)
			err = sqlDB.PingContext(ctx)
			cancel()
			if err == nil {
				checks["database"] = "ok"
			}
		}
	}

	if readFile != nil {
		index, err := readFile("index.html")
		if err == nil && len(index) > 0 {
			checks["webui"] = "ok"
		}
	}

	return checks["database"] == "ok" && checks["webui"] == "ok", checks
}
