package middleware

import (
	"oneinstack/app"
	"strings"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders protects both the default HTTP access path and the optional
// HTTPS listener. HSTS is deliberately not enabled: it is host-wide and would
// make the supported HTTP entry point unusable, especially when HTTPS uses a
// separate non-443 port.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		headers := c.Writer.Header()
		headers.Set("X-Content-Type-Options", "nosniff")
		headers.Set("X-Frame-Options", "DENY")
		headers.Set("Referrer-Policy", "no-referrer")
		headers.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		headers.Set(
			"Content-Security-Policy",
			contentSecurityPolicy(),
		)
		c.Next()
	}
}

func contentSecurityPolicy() string {
	styleSources := []string{"'self'"}
	if app.ONE_CONFIG.System.AllowInlineStyle {
		styleSources = append(styleSources, "'unsafe-inline'")
	}

	connectSources := []string{"'self'", "wss:"}
	connectSources = append(connectSources, "ws:")

	return "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; " +
		"img-src 'self' data: blob:; font-src 'self' data:; " +
		"style-src " + strings.Join(styleSources, " ") + "; " +
		"script-src 'self'; connect-src " + strings.Join(connectSources, " ")
}
