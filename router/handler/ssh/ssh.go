package ssh

import (
	"oneinstack/app"
	"oneinstack/core"
	sshservice "oneinstack/internal/services/ssh"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/middleware"
	panelServer "oneinstack/server"
	"time"

	"github.com/gin-gonic/gin"
)

func CreateTicket(c *gin.Context) {
	if !app.ONE_CONFIG.System.TerminalEnabled {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端未启用"))
		return
	}
	if !requestAllowsTerminal(c) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端仅支持 HTTPS/WSS 访问"))
		return
	}
	var input struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "请求参数错误"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "用户身份无效"))
		return
	}
	usernameValue, ok := c.Get(middleware.ContextUsername)
	username, validUsername := usernameValue.(string)
	if !ok || !validUsername || username == "" {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "用户身份无效"))
		return
	}
	account, verified := userservice.CheckUserPassword(username, input.Password)
	if !verified || account.ID != userID {
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "二次认证失败"))
		return
	}

	ticket, expiresAt, err := sshservice.DefaultTickets.Issue(sshservice.TicketClaims{
		UserID:   userID,
		Username: account.Username,
		ClientIP: c.ClientIP(),
	})
	if err != nil {
		core.HandleError(c, core.NewError(core.ErrInternalError, "创建终端票据失败"))
		return
	}
	core.HandleSuccess(c, gin.H{
		"ticket":    ticket,
		"expiresAt": expiresAt,
	})
}

func OpenSSH(c *gin.Context) {
	if !app.ONE_CONFIG.System.TerminalEnabled {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端未启用"))
		return
	}
	if !requestAllowsTerminal(c) {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端仅支持 HTTPS/WSS 访问"))
		return
	}
	if _, ok := middleware.AuthenticatedUserID(c); !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "终端票据无效"))
		return
	}
	sessionDuration := time.Duration(app.ONE_CONFIG.System.TerminalSessionMins) * time.Minute
	if sessionDuration <= 0 || sessionDuration > 2*time.Hour {
		sessionDuration = 15 * time.Minute
	}
	sshservice.OpenWebShell(c, sessionDuration)
}

func requestAllowsTerminal(c *gin.Context) bool {
	return panelServer.RequestIsHTTPS(c.Request, app.ONE_CONFIG.System.TrustedProxies)
}
