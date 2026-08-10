package ssh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"syscall"
	"time"

	"oneinstack/app"
	securityservice "oneinstack/internal/services/security"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	terminalReadLimit       = 64 << 10
	terminalPingInterval    = 20 * time.Second
	terminalSessionCheck    = 15 * time.Second
	terminalProcessStopWait = 2 * time.Second
	terminalWriteTimeout    = 5 * time.Second
	terminalMaxOutputBytes  = 32 << 20
)

func OpenWebShell(
	c *gin.Context,
	policy TerminalPolicy,
	claims TerminalSessionClaims,
) error {
	sessionContext, cancel := context.WithTimeout(c.Request.Context(), policy.MaxDuration)
	defer cancel()

	command, cleanupTerminalRC, err := terminalCommand(sessionContext, policy)
	if err != nil {
		return err
	}
	defer cleanupTerminalRC()
	session, err := DefaultSessions.Acquire(claims, policy)
	if err != nil {
		return err
	}
	closeReason := "client_closed"
	var reasonMu sync.Mutex
	defer func() {
		reasonMu.Lock()
		reason := closeReason
		reasonMu.Unlock()
		session.Close(reason)
	}()

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     sameOrigin,
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		closeReason = "upgrade_failed"
		return err
	}
	defer connection.Close()
	connection.SetReadLimit(terminalReadLimit)
	var writeMu sync.Mutex

	terminal, err := pty.StartWithAttrs(
		command,
		&pty.Winsize{Rows: 30, Cols: 100},
		&syscall.SysProcAttr{Setsid: true, Setctty: true},
	)
	if err != nil {
		closeReason = "process_start_failed"
		writeTerminalNotice(connection, &writeMu, "无法启动 root 终端会话")
		return fmt.Errorf("start root terminal: %w", err)
	}
	defer terminal.Close()
	defer stopTerminalProcess(command.Process, command.Wait)

	writeTerminalNotice(
		connection,
		&writeMu,
		"\x1b[38;5;45m◆ Root 终端已连接\x1b[0m  \x1b[38;5;245m当前会话拥有完整系统权限，操作事件将写入审计日志。\x1b[0m\r\n",
	)

	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func(reason string) {
		doneOnce.Do(func() {
			reasonMu.Lock()
			closeReason = reason
			reasonMu.Unlock()
			close(done)
			cancel()
			_ = connection.Close()
		})
	}

	go copyTerminalOutput(connection, &writeMu, terminal, session, finish)
	go enforceTerminalLifetime(
		sessionContext,
		connection,
		&writeMu,
		session,
		claims,
		policy.IdleTimeout,
		finish,
	)

	for {
		messageType, data, readErr := connection.ReadMessage()
		if readErr != nil {
			if errors.Is(sessionContext.Err(), context.DeadlineExceeded) {
				finish("duration_limit")
			} else {
				finish("client_closed")
			}
			break
		}
		session.Touch()

		switch messageType {
		case websocket.TextMessage:
			if handleResizeMessage(terminal, data) {
				continue
			}
			decoded, decodeErr := base64.StdEncoding.DecodeString(string(data))
			if decodeErr != nil {
				finish("invalid_input")
				break
			}
			if recordErr := session.RecordInput(decoded, terminalInputVisible(terminal)); recordErr != nil {
				writeTerminalNotice(
					connection,
					&writeMu,
					"\r\n审计链不可写，本次命令未执行，终端已关闭。\r\n",
				)
				finish("audit_failed")
				break
			}
			if _, writeErr := terminal.Write(decoded); writeErr != nil {
				finish("pty_write_failed")
				break
			}
		case websocket.BinaryMessage:
			if recordErr := session.RecordInput(data, terminalInputVisible(terminal)); recordErr != nil {
				writeTerminalNotice(
					connection,
					&writeMu,
					"\r\n审计链不可写，本次命令未执行，终端已关闭。\r\n",
				)
				finish("audit_failed")
				break
			}
			if _, writeErr := terminal.Write(data); writeErr != nil {
				finish("pty_write_failed")
				break
			}
		default:
			finish("unsupported_message")
		}
		select {
		case <-done:
			return nil
		default:
		}
	}
	<-done
	return nil
}

func copyTerminalOutput(
	connection *websocket.Conn,
	writeMu *sync.Mutex,
	terminal *os.File,
	session *TerminalSession,
	finish func(string),
) {
	buffer := make([]byte, 4096)
	for {
		count, readErr := terminal.Read(buffer)
		if count > 0 {
			if session.RecordOutput(count) > terminalMaxOutputBytes {
				writeTerminalNotice(
					connection,
					writeMu,
					"\r\n终端输出已达到单会话上限，会话已关闭。\r\n",
				)
				finish("output_limit")
				return
			}
			encoded := base64.StdEncoding.EncodeToString(buffer[:count])
			writeMu.Lock()
			_ = connection.SetWriteDeadline(time.Now().Add(terminalWriteTimeout))
			writeErr := connection.WriteMessage(
				websocket.TextMessage,
				[]byte(encoded),
			)
			writeMu.Unlock()
			if writeErr != nil {
				finish("client_write_failed")
				return
			}
		}
		if readErr != nil {
			finish("process_exited")
			return
		}
	}
}

func enforceTerminalLifetime(
	ctx context.Context,
	connection *websocket.Conn,
	writeMu *sync.Mutex,
	session *TerminalSession,
	claims TerminalSessionClaims,
	idleTimeout time.Duration,
	finish func(string),
) {
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Minute
	}
	pingTicker := time.NewTicker(terminalPingInterval)
	defer pingTicker.Stop()
	sessionTicker := time.NewTicker(terminalSessionCheck)
	defer sessionTicker.Stop()
	idleTicker := time.NewTicker(time.Second)
	defer idleTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			finish("duration_limit")
			return
		case <-pingTicker.C:
			deadline := time.Now().Add(5 * time.Second)
			if err := connection.WriteControl(
				websocket.PingMessage,
				[]byte("terminal"),
				deadline,
			); err != nil {
				finish("client_unreachable")
				return
			}
		case <-idleTicker.C:
			if session.IdleFor() >= idleTimeout {
				writeTerminalNotice(connection, writeMu, "\r\n会话因长时间无输入已关闭。\r\n")
				finish("idle_timeout")
				return
			}
		case <-sessionTicker.C:
			if !sourceSessionValid(claims) {
				writeTerminalNotice(connection, writeMu, "\r\n主登录会话已失效，终端已关闭。\r\n")
				finish("source_session_revoked")
				return
			}
		}
	}
}

func sourceSessionValid(claims TerminalSessionClaims) bool {
	if claims.SourceSessionID == "" || claims.UserID <= 0 {
		return false
	}
	database := app.DB()
	if database == nil {
		return false
	}
	manager := securityservice.NewSessionManager(database)
	_, err := manager.Validate(
		claims.SourceSessionID,
		claims.UserID,
		claims.SecurityVersion,
	)
	return err == nil
}

func stopTerminalProcess(process *os.Process, wait func() error) {
	if process == nil {
		return
	}
	_ = syscall.Kill(-process.Pid, syscall.SIGTERM)
	waited := make(chan struct{})
	go func() {
		_ = wait()
		close(waited)
	}()
	timer := time.NewTimer(terminalProcessStopWait)
	defer timer.Stop()
	select {
	case <-waited:
	case <-timer.C:
		_ = syscall.Kill(-process.Pid, syscall.SIGKILL)
		<-waited
	}
}

func writeTerminalNotice(
	connection *websocket.Conn,
	writeMu *sync.Mutex,
	message string,
) {
	if connection == nil {
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(message))
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = connection.SetWriteDeadline(time.Now().Add(terminalWriteTimeout))
	_ = connection.WriteMessage(websocket.TextMessage, []byte(encoded))
}

func handleResizeMessage(terminal *os.File, data []byte) bool {
	var size struct {
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if err := json.Unmarshal(data, &size); err != nil {
		return false
	}
	if size.Rows < 1 || size.Rows > 500 || size.Cols < 1 || size.Cols > 500 {
		return true
	}
	_ = pty.Setsize(terminal, &pty.Winsize{Rows: size.Rows, Cols: size.Cols})
	return true
}

func sameOrigin(request *http.Request) bool {
	origin := request.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil {
		return false
	}
	return parsed.Host == request.Host
}
