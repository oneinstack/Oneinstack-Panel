package ssh

import (
	"net/http"
	"oneinstack/internal/services/ssh"
	"oneinstack/utils"

	"github.com/gin-gonic/gin"
)

func OpenSSH(c *gin.Context) {
	authHeader := c.Query("Authorization")
	if authHeader == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no token"})
		c.Abort()
		return
	}
	_, err := utils.ValidateJWT(authHeader)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid token"})
		c.Abort()
		return
	}
	ssh.OpenWebShell(c)
}
