package middleware

import (
	"oneinstack/internal/i18n"

	"github.com/gin-gonic/gin"
)

const ContextLocale = "locale"

func Locale() gin.HandlerFunc {
	return func(c *gin.Context) {
		locale := i18n.FromRequest(c.Request)
		c.Set(ContextLocale, locale)
		c.Header("Content-Language", locale)
		c.Next()
	}
}

func RequestLocale(c *gin.Context) string {
	locale, ok := c.Get(ContextLocale)
	if !ok {
		return i18n.LocaleZhCN
	}
	return i18n.Canonical(locale.(string))
}
