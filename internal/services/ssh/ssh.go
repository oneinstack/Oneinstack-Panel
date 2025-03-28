package ssh

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
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
	cmd := exec.Command("bash", "-i") // 加 -i 更像交互式终端
	cmd.Env = append(os.Environ(), "TERM=xterm")
	fmt.Println(cmd.Args)
	fmt.Println(cmd.String())
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Rows: 24, Cols: 80}, &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
	})
	fmt.Println("ptmx:", ptmx)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to start shell: "+err.Error()))
		return
	}
	defer ptmx.Close()
	defer cmd.Process.Kill()

	// 设置终端属性，关闭 echo
	go func() {
		time.Sleep(100 * time.Millisecond)
		stty := exec.Command("stty", "-echo")
		stty.Stdin = ptmx
		stty.Stdout = ptmx
		stty.Stderr = ptmx
		stty.Run()
	}()

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

			if err := conn.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
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
		for {
			// 设置30秒读取超时
			conn.SetReadDeadline(time.Now().Add(10 * time.Minute))
			messageType, data, err := conn.ReadMessage()
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Println("Connection closed due to inactivity timeout")
				}
				closeDone()
				return
			}

			switch messageType {

			case websocket.TextMessage:
				// 先尝试解析窗口大小
				var size struct {
					Rows uint16 `json:"rows"`
					Cols uint16 `json:"cols"`
				}
				if err := json.Unmarshal(data, &size); err == nil {
					pty.Setsize(ptmx, &pty.Winsize{
						Rows: size.Rows,
						Cols: size.Cols,
					})
					continue
				}
				// data 是base64编码的字符串 通过base64解码
				ddata, err := base64.StdEncoding.DecodeString(string(data))
				if err != nil {
					return
				}
				// 处理普通文本输入
				if _, err := ptmx.Write(ddata); err != nil {
					return
				}

			case websocket.BinaryMessage:
				// data 是base64编码的字符串 通过base64解码
				ddata, err := base64.StdEncoding.DecodeString(string(data))
				if err != nil {
					return
				}
				// 直接写入二进制数据
				if _, err := ptmx.Write(ddata); err != nil {
					return
				}
			default:
				closeDone()
				return
			}

		}
	}()

	// 等待关闭信号
	<-done
}
