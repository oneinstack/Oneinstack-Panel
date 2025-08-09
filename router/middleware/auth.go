package middleware

import (
	"net/http"
	"oneinstack/utils"
	"strings"

	"github.com/gin-gonic/gin"
)

func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization header is required",
				"code":  "MISSING_TOKEN",
			})
			c.Abort()
			return
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid authorization header format. Expected: Bearer <token>",
				"code":  "INVALID_TOKEN_FORMAT",
			})
			c.Abort()
			return
		}

		token := parts[1]
		if len(token) == 0 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Token cannot be empty",
				"code":  "EMPTY_TOKEN",
			})
			c.Abort()
			return
		}

		claims, err := utils.ValidateJWT(token)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error":  "Invalid or expired token",
				"code":   "INVALID_TOKEN",
				"detail": err.Error(),
			})
			c.Abort()
			return
		}

		// 设置用户上下文信息
		c.Set("username", claims.Username)
		c.Set("userId", claims.Id)
		c.Set("tokenClaims", claims)

		c.Next()
	}
}
