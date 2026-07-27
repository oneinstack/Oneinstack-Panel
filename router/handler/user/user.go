package user

import (
	"errors"
	"oneinstack/app"
	"oneinstack/core"
	"oneinstack/internal/models"
	auditservice "oneinstack/internal/services/audit"
	"oneinstack/internal/services/log"
	securityservice "oneinstack/internal/services/security"
	"oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/router/middleware"
	"oneinstack/router/session"
	"oneinstack/utils"
	"time"

	"github.com/gin-gonic/gin"
)

func LoginHandler(c *gin.Context) {
	var req input.LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		appErr := core.NewErrorWithDetail(core.ErrBadRequest, "请求参数格式错误", err.Error())
		core.HandleError(c, appErr)
		log.CreateLog(&models.SystemLog{
			LogType:  models.Login_Type,
			Content:  "登录失败-请求参数错误",
			LogInfo:  0,
			IP:       c.ClientIP(),
			Agent:    c.GetHeader("User-Agent"),
			UserName: req.Username,
		})
		return
	}
	c.Set(middleware.ContextUsername, req.Username)

	// 验证用户名格式
	if err := utils.ValidateUsername(req.Username); err != nil {
		core.HandleError(c, err)
		log.CreateLog(&models.SystemLog{
			LogType:  models.Login_Type,
			Content:  "登录失败-用户名格式错误",
			LogInfo:  0,
			IP:       c.ClientIP(),
			Agent:    c.GetHeader("User-Agent"),
			UserName: req.Username,
		})
		return
	}

	// 验证密码不为空
	if req.Password == "" {
		appErr := core.NewFieldError(core.ErrBadRequest, "密码不能为空", "password")
		core.HandleError(c, appErr)
		log.CreateLog(&models.SystemLog{
			LogType:  models.Login_Type,
			Content:  "登录失败-密码为空",
			LogInfo:  0,
			IP:       c.ClientIP(),
			Agent:    c.GetHeader("User-Agent"),
			UserName: req.Username,
		})
		return
	}

	account, ok := user.CheckUserPassword(req.Username, req.Password)
	if !ok {
		appErr := core.NewError(core.ErrInvalidPassword, "用户名或密码错误")
		core.HandleError(c, appErr)
		log.CreateLog(&models.SystemLog{
			LogType:  models.Login_Type,
			Content:  "登录失败-密码错误",
			LogInfo:  0,
			IP:       c.ClientIP(),
			Agent:    c.GetHeader("User-Agent"),
			UserName: req.Username,
		})
		return
	}

	totpManager := securityservice.NewTOTPManager(app.DB())
	totpEnabled, err := totpManager.IsEnabled(account.ID)
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "读取二次认证状态失败"))
		return
	}
	if totpEnabled && req.TOTPCode == "" {
		core.HandleSuccess(c, gin.H{
			"authenticated":     false,
			"requiresTwoFactor": true,
		})
		return
	}
	if totpEnabled {
		if err := totpManager.VerifyLoginCode(account.ID, req.TOTPCode); err != nil {
			appErr := core.NewError(core.ErrInvalidPassword, "动态口令或恢复码无效")
			if !errors.Is(err, securityservice.ErrInvalidSecondFactor) {
				appErr = core.WrapError(err, core.ErrInternalError, "校验二次认证失败")
			}
			core.HandleError(c, appErr)
			log.CreateLog(&models.SystemLog{
				LogType: models.Login_Type, Content: "登录失败-二次认证错误",
				LogInfo: 0, IP: c.ClientIP(), Agent: c.GetHeader("User-Agent"),
				UserName: req.Username,
			})
			return
		}
	}

	sessionManager := securityservice.NewSessionManager(app.DB())
	sessionRecord, err := sessionManager.Create(securityservice.NewSession{
		UserID: account.ID, Username: account.Username,
		RemoteIP: auditservice.RemoteIP(c.Request), UserAgent: c.GetHeader("User-Agent"),
		SecurityVersion: account.EffectiveSecurityVersion(),
		ExpiresAt:       time.Now().Add(session.MaxAge),
	})
	if err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "创建登录会话失败"))
		return
	}
	// JWT 只写入 HttpOnly Cookie，不再暴露给前端脚本。
	token, _, err := utils.GenerateSessionJWT(
		account.Username, account.ID, sessionRecord.ID, account.EffectiveSecurityVersion(),
	)
	if err != nil {
		_, _ = sessionManager.Revoke(account.ID, sessionRecord.ID, "token_generation_failed")
		appErr := core.WrapError(err, core.ErrInternalError, "生成访问令牌失败")
		core.HandleError(c, appErr)
		log.CreateLog(&models.SystemLog{
			LogType:  models.Login_Type,
			Content:  "登录失败-生成Token错误",
			LogInfo:  0,
			IP:       c.ClientIP(),
			Agent:    c.GetHeader("User-Agent"),
			UserName: req.Username,
		})
		return
	}
	session.Write(c, token)
	c.Set(middleware.ContextUserID, account.ID)
	c.Set(middleware.ContextAuthMode, middleware.AuthModeCookie)
	c.Set(middleware.ContextSessionID, sessionRecord.ID)

	// 记录成功登录日志
	log.CreateLog(&models.SystemLog{
		LogType:  models.Login_Type,
		Content:  "登录成功",
		IP:       c.ClientIP(),
		LogInfo:  1,
		Agent:    c.GetHeader("User-Agent"),
		UserName: req.Username,
	})

	// 返回成功响应
	core.HandleSuccess(c, gin.H{
		"authenticated":      true,
		"firstLogin":         account.MustChangePassword,
		"mustChangePassword": account.MustChangePassword,
		"requiresTwoFactor":  false,
		"user": gin.H{
			"id":          account.ID,
			"username":    account.Username,
			"isAdmin":     account.IsAdmin,
			"totpEnabled": totpEnabled,
		},
	})
}

func LogoutHandler(c *gin.Context) {
	if userID, ok := middleware.AuthenticatedUserID(c); ok {
		if sessionID, ok := middleware.AuthenticatedSessionID(c); ok {
			_, _ = securityservice.NewSessionManager(app.DB()).
				Revoke(userID, sessionID, "logout")
		}
	}
	session.Clear(c)
	core.HandleSuccess(c, gin.H{"authenticated": false})
}
