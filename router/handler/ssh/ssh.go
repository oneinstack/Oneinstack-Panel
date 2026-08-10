package ssh

import (
	"errors"
	"net/http"
	"oneinstack/app"
	"oneinstack/core"
	sshservice "oneinstack/internal/services/ssh"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func CreateTicket(c *gin.Context) {
	if !app.ONE_CONFIG.System.TerminalEnabled {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端未启用"))
		return
	}
	securityStatus := sshservice.GetTerminalSecurityStatus()
	if !securityStatus.Available {
		core.HandleErrorWithStatus(
			c,
			http.StatusServiceUnavailable,
			core.NewErrorWithDetail(
				core.ErrSystemError,
				"终端运行环境不可用",
				securityStatus.Reason,
			),
		)
		return
	}
	var input struct {
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		core.HandleError(c, core.WrapError(err, core.ErrBadRequest, "SSH 会话票据参数格式不正确"))
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
	sourceSessionID, ok := middleware.AuthenticatedSessionID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "主登录会话无效"))
		return
	}

	ticket, expiresAt, err := sshservice.DefaultTickets.Issue(sshservice.TicketClaims{
		UserID:          userID,
		Username:        account.Username,
		ClientIP:        c.ClientIP(),
		UserAgent:       c.GetHeader("User-Agent"),
		SourceSessionID: sourceSessionID,
		SecurityVersion: account.EffectiveSecurityVersion(),
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

func Status(c *gin.Context) {
	core.HandleSuccess(c, sshservice.GetTerminalSecurityStatus())
}

func Sessions(c *gin.Context) {
	core.HandleSuccess(c, sshservice.DefaultSessions.List())
}

func OpenSSH(c *gin.Context) {
	if !app.ONE_CONFIG.System.TerminalEnabled {
		core.HandleError(c, core.NewError(core.ErrForbidden, "Web 终端未启用"))
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "终端票据无效"))
		return
	}
	usernameValue, _ := c.Get(middleware.ContextUsername)
	username, _ := usernameValue.(string)
	ticketValue, _ := c.Get(middleware.ContextTokenClaims)
	ticket, ok := ticketValue.(sshservice.TicketClaims)
	if !ok || ticket.UserID != userID || ticket.Username != username {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "终端票据声明无效"))
		return
	}
	err := sshservice.OpenWebShell(c, sshservice.CurrentTerminalPolicy(), sshservice.TerminalSessionClaims{
		UserID: userID, Username: username, ClientIP: c.ClientIP(),
		UserAgent:       c.GetHeader("User-Agent"),
		SourceSessionID: ticket.SourceSessionID,
		SecurityVersion: ticket.SecurityVersion,
	})
	if err == nil || c.Writer.Written() {
		return
	}
	if errors.Is(err, sshservice.ErrTerminalCapacity) {
		core.HandleErrorWithStatus(
			c,
			http.StatusTooManyRequests,
			core.NewError(core.ErrRateLimitExceeded, "终端并发会话已达到上限"),
		)
		return
	}
	if errors.Is(err, sshservice.ErrTerminalUnavailable) {
		core.HandleErrorWithStatus(
			c,
			http.StatusServiceUnavailable,
			core.NewErrorWithDetail(core.ErrSystemError, "终端运行环境不可用", err.Error()),
		)
		return
	}
	if errors.Is(err, sshservice.ErrTerminalAuditUnavailable) {
		core.HandleErrorWithStatus(
			c,
			http.StatusServiceUnavailable,
			core.NewError(core.ErrSystemError, "终端审计链不可用"),
		)
		return
	}
	core.HandleError(c, core.WrapError(err, core.ErrInternalError, "终端会话启动失败"))
}
