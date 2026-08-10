package container

import (
	"errors"
	"net/http"
	"strings"

	"oneinstack/core"
	containerService "oneinstack/internal/services/container"
	userservice "oneinstack/internal/services/user"
	"oneinstack/router/input"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func TerminalStatus(c *gin.Context) {
	if !requireContainerTerminalTransport(c) {
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	status, err := service.ContainerTerminalStatus(ctx, c.Param("id"))
	if err == nil || errors.Is(err, containerService.ErrContainerTerminalDisabled) ||
		errors.Is(err, containerService.ErrContainerNotRunning) ||
		errors.Is(err, containerService.ErrContainerShellUnavailable) {
		core.HandleSuccess(c, status)
		return
	}
	recordAction(c, "container.terminal.status", terminalErrorStatus(err), err)
	terminalError(c, err)
}

func CreateTerminalTicket(c *gin.Context) {
	if !requireContainerTerminalTransport(c) {
		return
	}
	var request input.ContainerTerminalTicketRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		badRequest(c, err)
		return
	}
	ctx, cancel := requestContext(c)
	defer cancel()
	target, err := service.PrepareContainerTerminal(ctx, c.Param("id"))
	if err != nil {
		recordAction(c, "container.terminal.ticket", terminalErrorStatus(err), err)
		terminalError(c, err)
		return
	}

	userID, ok := middleware.AuthenticatedUserID(c)
	username := c.GetString(middleware.ContextUsername)
	if !ok || username == "" {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "用户身份无效"))
		return
	}
	account, verified := userservice.CheckUserPassword(username, request.Password)
	if !verified || account == nil || account.ID != userID {
		err = errors.New("容器终端二次认证失败")
		recordAction(c, "container.terminal.ticket", http.StatusUnauthorized, err)
		core.HandleError(c, core.NewError(core.ErrInvalidPassword, "二次认证失败"))
		return
	}
	sourceSessionID, ok := middleware.AuthenticatedSessionID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "主登录会话无效"))
		return
	}
	if len(target.Risks) > 0 && !request.ConfirmHighRisk {
		riskMessages := make([]string, 0, len(target.Risks))
		for _, risk := range target.Risks {
			riskMessages = append(riskMessages, risk.Message)
		}
		err = errors.New(strings.Join(riskMessages, "；"))
		recordAction(c, "container.terminal.ticket", http.StatusConflict, err)
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewErrorWithDetail(
			core.ErrOperationNotConfirmed,
			"该容器具有高风险运行配置，请确认风险后重试",
			strings.Join(riskMessages, "；")+"。确认后请提交 confirmHighRisk=true。",
		))
		return
	}

	ticket, expiresAt, err := containerService.DefaultTerminalTickets.Issue(containerService.ContainerTerminalTicketClaims{
		UserID: userID, Username: username, ClientIP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"),
		SourceSessionID: sourceSessionID, SecurityVersion: account.EffectiveSecurityVersion(),
		ContainerReference: strings.TrimSpace(c.Param("id")), ContainerID: target.ID,
		ContainerName: target.Name, Shell: target.Shell, HighRiskConfirmed: request.ConfirmHighRisk,
	})
	if err != nil {
		recordAction(c, "container.terminal.ticket", http.StatusInternalServerError, err)
		core.HandleError(c, core.NewError(core.ErrInternalError, "创建容器终端票据失败"))
		return
	}
	recordAction(c, "container.terminal.ticket", http.StatusOK, nil)
	core.HandleSuccess(c, gin.H{
		"ticket": ticket, "expiresAt": expiresAt, "containerId": target.ID,
		"containerName": target.Name, "shell": target.Shell,
	})
}

func OpenTerminal(c *gin.Context) {
	if !requireContainerTerminalTransport(c) {
		return
	}
	userID, ok := middleware.AuthenticatedUserID(c)
	if !ok {
		core.HandleError(c, core.NewError(core.ErrUnauthorized, "容器终端票据无效"))
		return
	}
	value, _ := c.Get(middleware.ContextTokenClaims)
	ticket, ok := value.(containerService.ContainerTerminalTicketClaims)
	if !ok || ticket.UserID != userID || ticket.Username != c.GetString(middleware.ContextUsername) ||
		ticket.ContainerReference != strings.TrimSpace(c.Param("id")) {
		core.HandleError(c, core.NewError(core.ErrInvalidTerminalTicket, "容器终端票据声明无效"))
		return
	}

	ctx, cancel := requestContext(c)
	target, err := service.ValidateContainerTerminalTarget(ctx, ticket.ContainerID, ticket.Shell)
	cancel()
	if err != nil {
		recordAction(c, "container.terminal.open", terminalErrorStatus(err), err)
		terminalError(c, err)
		return
	}
	if len(target.Risks) > 0 && !ticket.HighRiskConfirmed {
		err = errors.New("高风险容器终端未确认")
		recordAction(c, "container.terminal.open", http.StatusConflict, err)
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrOperationNotConfirmed, "高风险容器终端需要重新确认"))
		return
	}

	err = service.OpenContainerTerminal(c, target, containerService.ContainerTerminalSessionClaims{
		UserID: userID, Username: ticket.Username, ClientIP: c.ClientIP(), UserAgent: c.GetHeader("User-Agent"),
		SourceSessionID: ticket.SourceSessionID, SecurityVersion: ticket.SecurityVersion,
		ContainerID: target.ID, ContainerName: target.Name, Shell: target.Shell, RiskConfirmed: ticket.HighRiskConfirmed,
	})
	if err == nil {
		return
	}
	recordAction(c, "container.terminal.open", terminalErrorStatus(err), err)
	if c.Writer.Written() {
		return
	}
	terminalError(c, err)
}

func terminalErrorStatus(err error) int {
	switch {
	case errors.Is(err, containerService.ErrContainerTerminalDisabled):
		return http.StatusForbidden
	case errors.Is(err, containerService.ErrContainerNotRunning), errors.Is(err, containerService.ErrContainerShellUnavailable):
		return http.StatusConflict
	case errors.Is(err, containerService.ErrContainerNotFound):
		return http.StatusNotFound
	case errors.Is(err, containerService.ErrRuntimeUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, containerService.ErrContainerTerminalCapacity):
		return http.StatusTooManyRequests
	case errors.Is(err, containerService.ErrContainerTerminalAudit):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

func terminalError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, containerService.ErrContainerTerminalDisabled):
		core.HandleErrorWithStatus(c, http.StatusForbidden, core.NewError(core.ErrForbidden, "容器终端未启用"))
	case errors.Is(err, containerService.ErrContainerNotRunning):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrResourceStateInvalid, "容器未运行，无法打开终端"))
	case errors.Is(err, containerService.ErrContainerShellUnavailable):
		core.HandleErrorWithStatus(c, http.StatusConflict, core.NewError(core.ErrResourceStateInvalid, "容器中未找到可用的 /bin/bash 或 /bin/sh"))
	case errors.Is(err, containerService.ErrContainerNotFound):
		core.HandleErrorWithStatus(c, http.StatusNotFound, core.NewError(core.ErrNotFound, "目标容器不存在或已被删除"))
	case errors.Is(err, containerService.ErrRuntimeUnavailable):
		core.HandleErrorWithStatus(c, http.StatusServiceUnavailable, core.NewError(core.ErrContainerRuntimeUnavailable, "Docker 运行时不可用"))
	case errors.Is(err, containerService.ErrContainerTerminalCapacity):
		core.HandleErrorWithStatus(c, http.StatusTooManyRequests, core.NewError(core.ErrRateLimitExceeded, "容器终端并发会话已达到上限"))
	case errors.Is(err, containerService.ErrContainerTerminalAudit):
		core.HandleErrorWithStatus(c, http.StatusServiceUnavailable, core.NewError(core.ErrServiceUnavailable, "容器终端审计链不可用"))
	case errors.Is(err, containerService.ErrInvalidContainerTicket), errors.Is(err, containerService.ErrExpiredContainerTicket):
		core.HandleErrorWithStatus(c, http.StatusUnauthorized, core.NewError(core.ErrInvalidTerminalTicket, "容器终端票据无效或已过期"))
	default:
		core.HandleError(c, core.WrapError(err, core.ErrInternalError, "容器终端操作失败"))
	}
}

func requireContainerTerminalTransport(c *gin.Context) bool {
	if containerService.ContainerTerminalTransportAllowed(c.Request) {
		return true
	}
	err := errors.New("容器终端仅允许通过 HTTPS/WSS 访问；开发环境请设置 GO_ENV=development 或使用 one debug 启动")
	recordAction(c, "container.terminal.transport", http.StatusForbidden, err)
	core.HandleErrorWithStatus(c, http.StatusForbidden, core.NewError(
		core.ErrForbidden,
		"容器终端仅允许通过 HTTPS/WSS 访问；开发环境请设置 GO_ENV=development 或使用 one debug 启动",
	))
	return false
}
