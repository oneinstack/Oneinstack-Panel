package middleware

import (
	"fmt"
	"net/http"
	"oneinstack/utils"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Permission 权限定义
type Permission string

const (
	PermissionSystemRead    Permission = "system:read"
	PermissionSystemWrite   Permission = "system:write"
	PermissionUserManage    Permission = "user:manage"
	PermissionSoftwareRead  Permission = "software:read"
	PermissionSoftwareWrite Permission = "software:write"
	PermissionWebsiteRead   Permission = "website:read"
	PermissionWebsiteWrite  Permission = "website:write"
	PermissionFirewallRead  Permission = "firewall:read"
	PermissionFirewallWrite Permission = "firewall:write"
	PermissionSSHAccess     Permission = "ssh:access"
	PermissionCronRead      Permission = "cron:read"
	PermissionCronWrite     Permission = "cron:write"
	PermissionFileRead      Permission = "file:read"
	PermissionFileWrite     Permission = "file:write"
)

// RequirePermission 权限验证中间件
func RequirePermission(requiredPermission Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 获取token信息
		tokenClaims, exists := c.Get("tokenClaims")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication required",
				"code":  "AUTH_REQUIRED",
			})
			c.Abort()
			return
		}

		claims, ok := tokenClaims.(*utils.Claims)
		if !ok {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid token claims",
				"code":  "INVALID_CLAIMS",
			})
			c.Abort()
			return
		}

		// 检查用户权限
		if !hasPermission(claims, requiredPermission) {
			c.JSON(http.StatusForbidden, gin.H{
				"error":               "Insufficient permissions",
				"code":                "INSUFFICIENT_PERMISSIONS",
				"required_permission": string(requiredPermission),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// RequireAdmin 需要管理员权限的中间件
func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		userId, exists := c.Get("userId")
		if !exists {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication required",
				"code":  "AUTH_REQUIRED",
			})
			c.Abort()
			return
		}

		// 这里应该从数据库查询用户角色，简化处理假设用户ID为1的是管理员
		userIdInt, ok := userId.(int64)
		if !ok || userIdInt != 1 {
			c.JSON(http.StatusForbidden, gin.H{
				"error": "Administrator access required",
				"code":  "ADMIN_REQUIRED",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

// hasPermission 检查用户是否具有特定权限
func hasPermission(claims *utils.Claims, permission Permission) bool {
	// 简化的权限检查逻辑
	// 在实际应用中，应该从数据库查询用户角色和权限

	// 假设用户ID为1的是超级管理员，拥有所有权限
	if claims.Id == 1 {
		return true
	}

	// 这里可以根据实际需求实现更复杂的权限逻辑
	// 例如：从数据库查询用户角色，然后检查角色权限

	return false
}

// AuditLog 审计日志中间件
func AuditLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 记录请求开始时间
		start := time.Now()

		// 获取用户信息
		username, _ := c.Get("username")
		userId, _ := c.Get("userId")

		// 处理请求
		c.Next()

		// 记录审计日志
		duration := time.Since(start)

		logEntry := map[string]interface{}{
			"timestamp":   start.Format(time.RFC3339),
			"method":      c.Request.Method,
			"path":        c.Request.URL.Path,
			"status":      c.Writer.Status(),
			"duration_ms": duration.Milliseconds(),
			"ip":          c.ClientIP(),
			"user_agent":  c.GetHeader("User-Agent"),
			"username":    username,
			"user_id":     userId,
		}

		// 记录敏感操作
		if isSensitiveOperation(c.Request.Method, c.Request.URL.Path) {
			logEntry["sensitive"] = true
			logEntry["request_body_size"] = c.Request.ContentLength
		}

		// 这里应该将日志写入到日志系统或数据库
		// 简化处理，输出到控制台
		fmt.Printf("AUDIT: %+v\n", logEntry)
	}
}

// isSensitiveOperation 判断是否为敏感操作
func isSensitiveOperation(method, path string) bool {
	sensitiveOperations := map[string][]string{
		"POST": {
			"/v1/login",
			"/v1/sys/updateuser",
			"/v1/sys/resetpassword",
			"/v1/sys/updateport",
			"/v1/soft/install",
			"/v1/soft/remove",
			"/v1/safe/add",
			"/v1/safe/del",
			"/v1/website/add",
			"/v1/website/del",
		},
		"DELETE": {
			"/v1/sys/remark/del",
			"/v1/website/del",
			"/v1/safe/del",
		},
	}

	paths, exists := sensitiveOperations[method]
	if !exists {
		return false
	}

	for _, sensitivePath := range paths {
		if strings.HasPrefix(path, sensitivePath) {
			return true
		}
	}

	return false
}
