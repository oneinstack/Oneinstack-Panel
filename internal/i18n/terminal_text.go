package i18n

const (
	TerminalRootConnectedTitle  = "terminal.root.connected.title"
	TerminalRootConnectedDetail = "terminal.root.connected.detail"
	TerminalRootStartFailed     = "terminal.root.start_failed"
	TerminalAuditUnavailable    = "terminal.audit.unavailable"
	TerminalOutputLimit         = "terminal.output.limit"
	TerminalIdleTimeout         = "terminal.idle_timeout"
	TerminalSourceSessionClosed = "terminal.source_session.closed"
)

var terminalMessages = map[string]map[string]string{
	LocaleZhCN: {
		TerminalRootConnectedTitle:  "Root 终端已连接",
		TerminalRootConnectedDetail: "当前会话拥有完整系统权限，操作事件将写入审计日志。",
		TerminalRootStartFailed:     "无法启动 root 终端会话",
		TerminalAuditUnavailable:    "审计链不可写，本次命令未执行，终端已关闭。",
		TerminalOutputLimit:         "终端输出已达到单会话上限，会话已关闭。",
		TerminalIdleTimeout:         "会话因长时间无输入已关闭。",
		TerminalSourceSessionClosed: "主登录会话已失效，终端已关闭。",
	},
	LocaleEnUS: {
		TerminalRootConnectedTitle:  "Root terminal connected",
		TerminalRootConnectedDetail: "This session has full system privileges. Actions will be written to the audit log.",
		TerminalRootStartFailed:     "Unable to start the root terminal session",
		TerminalAuditUnavailable:    "The audit chain is not writable. The command was not executed and the terminal was closed.",
		TerminalOutputLimit:         "The terminal output reached the per-session limit. The session was closed.",
		TerminalIdleTimeout:         "The session was closed after a prolonged period without input.",
		TerminalSourceSessionClosed: "The primary login session is no longer valid. The terminal was closed.",
	},
}

// TerminalMessage returns a localized message for text written directly to
// the terminal WebSocket stream. Unknown locales use the existing Chinese
// compatibility fallback.
func TerminalMessage(locale, key string) string {
	if translated, ok := terminalMessages[Canonical(locale)][key]; ok {
		return translated
	}
	if translated, ok := terminalMessages[LocaleZhCN][key]; ok {
		return translated
	}
	return ""
}
