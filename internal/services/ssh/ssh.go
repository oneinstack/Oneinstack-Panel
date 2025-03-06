package ssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

const (
	pongWait   = 60 * time.Second
	pingPeriod = (pongWait * 9) / 10
)

func OpenWebShell(c *gin.Context) {
	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		fmt.Println("WebSocket upgrade failed:", err)
		return
	}
	defer conn.Close()

	// 创建带有伪终端的命令
	cmd := exec.Command("bash")
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Rows: 24, Cols: 80}, &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	})
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to start shell: "+err.Error()))
		return
	}
	defer ptmx.Close()
	defer cmd.Process.Kill()

	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }

	// 优化输出处理
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Println("Read error:", err)
			}
		}()
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				closeDone()
				return
			}

			// 编码为base64发送
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			if err := conn.WriteMessage(websocket.TextMessage, []byte(encoded)); err != nil {
				closeDone()
				return
			}
		}
	}()

	// 优化输入处理
	go func() {
		defer func() {
			if err := recover(); err != nil {
				log.Println("Read error:", err)
			}
		}()

		// 设置初始读取超时
		conn.SetReadDeadline(time.Now().Add(pongWait))
		conn.SetPongHandler(func(string) error {
			conn.SetReadDeadline(time.Now().Add(pongWait))
			return nil
		})

		for {
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Println("Connection closed due to inactivity timeout")
				}
				closeDone()
				return
			}

			switch messageType {
			case websocket.PingMessage:
				// 自动回复Pong消息
				conn.WriteMessage(websocket.PongMessage, nil)
			case websocket.TextMessage:
				// Base64解码输入
				decodedData, err := base64.StdEncoding.DecodeString(string(data))
				if err != nil {
					log.Println("Base64 decode error:", err)
					continue
				}

				// 先尝试解析窗口大小
				var size struct {
					Rows uint16 `json:"rows"`
					Cols uint16 `json:"cols"`
				}
				if err := json.Unmarshal(decodedData, &size); err == nil {
					pty.Setsize(ptmx, &pty.Winsize{
						Rows: size.Rows,
						Cols: size.Cols,
					})
					continue
				}

				// 处理普通文本输入
				if _, err := ptmx.Write(decodedData); err != nil {
					return
				}

			case websocket.BinaryMessage:
				// 解码二进制数据
				decodedData, err := base64.StdEncoding.DecodeString(string(data))
				if err != nil {
					log.Println("Base64 decode error:", err)
					continue
				}
				if _, err := ptmx.Write(decodedData); err != nil {
					return
				}
			default:
				closeDone()
				return
			}
		}
	}()

	// 添加心跳goroutine
	go func() {
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// 发送Ping消息
				if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
					closeDone()
					return
				}
			case <-done:
				return
			}
		}
	}()

	// 等待关闭信号
	<-done
}
