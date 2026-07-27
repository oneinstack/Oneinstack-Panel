package middleware

import (
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
			"default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; "+
				"img-src 'self' data: blob:; font-src 'self' data:; "+
				"style-src 'self' 'unsafe-inline'; script-src 'self'; "+
				"connect-src 'self' ws: wss:",
		)
		c.Next()
	}
}
