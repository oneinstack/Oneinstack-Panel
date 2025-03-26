package user

import (
	"net/http"
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
		core.HandleError(c, http.StatusUnauthorized, core.ErrBadRequest, err)
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

	user, ok := user.CheckUserPassword(req.Username, req.Password)
	if !ok {
		core.HandleError(c, http.StatusUnauthorized, core.ErrUnauthorizedAP, nil)
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
	// 设置用户首次登录状态
	token, err := utils.GenerateJWT(user.Username, user.ID)
	if err != nil {
		core.HandleError(c, http.StatusInternalServerError, core.ErrInternalServerError, gin.H{"error": "could not generate token"})
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
	log.CreateLog(&models.SystemLog{
		LogType:  models.Login_Type,
		Content:  "登录成功",
		IP:       c.ClientIP(),
		LogInfo:  1,
		Agent:    c.GetHeader("User-Agent"),
		UserName: req.Username,
	})
	core.HandleSuccess(c, gin.H{"token": token, "firstLogin": log.IsFirstLogin(req.Username)})
}
