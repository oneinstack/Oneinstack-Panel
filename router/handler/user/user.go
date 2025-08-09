package user

import (
	"oneinstack/core"
	"oneinstack/internal/models"
	"oneinstack/internal/services/log"
	"oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/utils"

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

	user, ok := user.CheckUserPassword(req.Username, req.Password)
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

	// 生成JWT令牌
	token, err := utils.GenerateJWT(user.Username, user.ID)
	if err != nil {
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
		"token":      token,
		"firstLogin": log.IsFirstLogin(req.Username),
		"user": gin.H{
			"id":       user.ID,
			"username": user.Username,
			"isAdmin":  user.IsAdmin,
		},
	})
}
