package ssh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const terminalIdleTimeout = 10 * time.Minute

func OpenWebShell(c *gin.Context, maxDuration time.Duration) {
	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     sameOrigin,
	}
	connection, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer connection.Close()
	connection.SetReadLimit(64 << 10)
	_ = connection.SetReadDeadline(time.Now().Add(terminalIdleTimeout))
	connection.SetPongHandler(func(string) error {
		return connection.SetReadDeadline(time.Now().Add(terminalIdleTimeout))
	})

	sessionContext, cancel := context.WithTimeout(c.Request.Context(), maxDuration)
	defer cancel()
	command := exec.CommandContext(sessionContext, "/bin/bash", "--noprofile", "--norc", "-i")
	command.Env = append(os.Environ(), "TERM=xterm-256color")
	terminal, err := pty.StartWithAttrs(command, &pty.Winsize{Rows: 30, Cols: 100}, &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	})
	if err != nil {
		_ = connection.WriteMessage(websocket.TextMessage, []byte(base64.StdEncoding.EncodeToString([]byte("无法启动终端会话\r\n"))))
		return
	}
	defer terminal.Close()
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()

	disableEcho := exec.Command("stty", "-echo")
	disableEcho.Stdin = terminal
	disableEcho.Stdout = terminal
	disableEcho.Stderr = terminal
	_ = disableEcho.Run()

	done := make(chan struct{})
	var doneOnce sync.Once
	finish := func() {
		doneOnce.Do(func() {
			close(done)
			_ = connection.Close()
		})
	}

	go func() {
		defer finish()
		buffer := make([]byte, 4096)
		for {
			count, readErr := terminal.Read(buffer)
			if count > 0 {
				encoded := base64.StdEncoding.EncodeToString(buffer[:count])
				if writeErr := connection.WriteMessage(websocket.TextMessage, []byte(encoded)); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	go func() {
		<-sessionContext.Done()
		finish()
	}()

	for {
		messageType, data, readErr := connection.ReadMessage()
		if readErr != nil {
			if networkError, ok := readErr.(net.Error); ok && networkError.Timeout() {
				finish()
			}
			break
		}
		_ = connection.SetReadDeadline(time.Now().Add(terminalIdleTimeout))

		switch messageType {
		case websocket.TextMessage:
			if handleResizeMessage(terminal, data) {
				continue
			}
			decoded, decodeErr := base64.StdEncoding.DecodeString(string(data))
			if decodeErr != nil {
				finish()
				break
			}
			if _, writeErr := terminal.Write(decoded); writeErr != nil {
				finish()
				break
			}
		case websocket.BinaryMessage:
			if _, writeErr := terminal.Write(data); writeErr != nil {
				finish()
				break
			}
		default:
			finish()
		}
		select {
		case <-done:
			return
		default:
		}
	}
	finish()
	<-done
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
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return parsed.Host == request.Host
}
