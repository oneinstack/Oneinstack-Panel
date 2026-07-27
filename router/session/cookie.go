package session

import (
	"net/http"
	"oneinstack/app"
	panelServer "oneinstack/server"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	CookieName = "oneinstack_session"
	MaxAge     = 24 * time.Hour
)

func Write(c *gin.Context, token string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/v1",
		MaxAge:   int(MaxAge.Seconds()),
		HttpOnly: true,
		Secure:   requestIsSecure(c.Request),
		SameSite: http.SameSiteStrictMode,
	})
}

func Clear(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/v1",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   requestIsSecure(c.Request),
		SameSite: http.SameSiteStrictMode,
	})
}

func requestIsSecure(r *http.Request) bool {
	return panelServer.RequestIsHTTPS(r, app.ONE_CONFIG.System.TrustedProxies)
}
