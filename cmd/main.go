package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"oneinstack/app"
	"oneinstack/internal/services/software"
	"oneinstack/internal/services/system"
	"oneinstack/internal/services/user"
	web "oneinstack/router"
	"oneinstack/router/input"
	"oneinstack/server"
	"oneinstack/utils"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var userName string
var password string
var initialized bool // 记录是否已经初始化

func main() {
	server.Start()
	//初始化服务
	resetPwdCmd.Flags().StringP("user", "u", "", "username")
	resetPwdCmd.Flags().StringP("password", "p", "", "new password")

	resetUserCmd.Flags().StringP("user", "u", "", "new username")

	changePortCmd.Flags().StringP("port", "p", "", "New port for the system")

	// 绑定 --user 和 --password 参数到 init 命令
	initCmd.Flags().StringVarP(&userName, "user", "u", "", "Specify the username")
	initCmd.Flags().StringVarP(&password, "password", "p", "", "Specify the password")

	// 确保用户名和密码参数是必填的
	initCmd.MarkFlagRequired("user")
	initCmd.MarkFlagRequired("password")

	// 将命令添加到根命令
	rootCmd.AddCommand(install)
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(resetPwdCmd)
	rootCmd.AddCommand(resetUserCmd)
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(changePortCmd)
	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(updateCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "one",
	Short: "oneinstack",
}

const pidFile = "server.pid" // 存储 PID 的文件路径

// 定义 initCmd 指令
var initCmd = &cobra.Command{
	Use:     "init",
	Short:   "Initialize the system with a username and password",
	Example: "go run main.go init --user=admin --password=123456",
	Run: func(cmd *cobra.Command, args []string) {
		// 初始化用户
		app.InitUser(userName, password)
	},
}

// serverStopCmd 定义启动和停止服务的命令
var serverCmd = &cobra.Command{
	Use:     "server",
	Short:   "Start, restart, or stop HTTP server",
	Example: "go run main.go server [start|restart|stop]",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) > 0 {
			switch args[0] {
			case "start":
				startServer()
			case "restart":
				restartServer()
			case "stop":
				stopServer()
			default:
				fmt.Println("Invalid argument. Use 'start', 'restart', or 'stop'.")
			}
		} else {
			fmt.Println("Invalid argument. Use 'start', 'restart', or 'stop'.")
		}
	},
}

// startServer 启动服务并记录 PID
func startServer() {
	r := web.SetupRouter()
	fmt.Println("HTTP Server starting...")

	// 创建 PID 文件
	pid := os.Getpid()
	err := os.WriteFile(app.GetBasePath()+pidFile, []byte(strconv.Itoa(pid)), 0644)
	if err != nil {
		log.Fatalf("Failed to write PID file: %v", err)
	}

	// 启动服务
	if app.ONE_CONFIG.System.Port == "" {
		app.ONE_CONFIG.System.Port = "8089"
	}
	ip, err := utils.GetLinuxIP()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("访问地址" + ip + ":" + app.ONE_CONFIG.System.Port)
	if err := r.Run("0.0.0.0:" + app.ONE_CONFIG.System.Port); err != nil {
		log.Fatal("Server run error:", err)
	}

	// 删除 PID 文件（在服务正常退出时）
	os.Remove(pidFile)
}

// restartServer 重启服务
func restartServer() {
	fmt.Println("Checking HTTP Server status...")

	// 检查是否存在 PID 文件
	if _, err := os.Stat(pidFile); os.IsNotExist(err) {
		fmt.Println("No running server found. Starting a new instance...")
		startServer()
		return
	}

	// 读取 PID 文件内容
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		log.Fatalf("Failed to read PID file: %v", err)
	}

	// 转换 PID 为整数
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		log.Fatalf("Invalid PID in file: %v", err)
	}

	// 检查进程是否存活
	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("No process found with PID %d. Starting a new instance...\n", pid)
		startServer()
		return
	}

	// 尝试向进程发送信号，确认进程是否存活
	err = process.Signal(syscall.Signal(0)) // 发送 0 信号用于检查进程
	if err != nil {
		fmt.Printf("No running server found for PID %d. Starting a new instance...\n", pid)
		startServer()
		return
	}

	// 如果进程存活，则停止当前服务
	fmt.Printf("Server is running with PID %d. Stopping it...\n", pid)
	stopServer()

	// 启动新服务
	startServer()
}

// stopServer 停止服务
func stopServer() {
	fmt.Println("Stopping HTTP Server...")

	// 读取 PID 文件
	pidData, err := os.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No running server found.")
			return
		}
		log.Fatalf("Failed to read PID file: %v", err)
	}

	// 转换 PID 并发送终止信号
	pid, err := strconv.Atoi(string(pidData))
	if err != nil {
		log.Fatalf("Invalid PID in file: %v", err)
	}

	// 向目标进程发送 SIGTERM 信号
	err = syscall.Kill(pid, syscall.SIGTERM)
	if err != nil {
		log.Fatalf("Failed to stop server: %v", err)
	}

	// 删除 PID 文件
	os.Remove(pidFile)
	fmt.Println("Server stopped successfully.")
}

var resetPwdCmd = &cobra.Command{
	Use:     "resetpwd",
	Short:   "Reset user password",
	Example: "one resetpwd -u admin -p newpassword",
	Run: func(cmd *cobra.Command, args []string) {
		username, _ := cmd.Flags().GetString("user")
		if username == "" {
			log.Fatalf("Username is required. Use: one resetpwd -u username -p password")
		}

		password, _ := cmd.Flags().GetString("password")
		if password == "" {
			log.Fatalf("Password is required. Use: one resetpwd -u username -p password")
		}

		err := user.ChangePassword(username, password)
		if err != nil {
			log.Fatalf("Reset password failed: %v", err)
		}

		fmt.Printf("✅ Password reset successfully for user: %s\n", username)
	},
}

var resetUserCmd = &cobra.Command{
	Use:     "resetuser",
	Short:   "Reset username",
	Example: "one resetuser -u newusername",
	Run: func(cmd *cobra.Command, args []string) {
		username, _ := cmd.Flags().GetString("user")
		if username == "" {
			log.Fatalf("Username is required. Use: one resetuser -u username")
		}

		err := user.ResetUsername(username)
		if err != nil {
			log.Fatalf("Reset username failed: %v", err)
		}

		fmt.Printf("✅ Username reset successfully to: %s\n", username)
	},
}

var install = &cobra.Command{
	Use:     "install",
	Short:   "安装 php nginx phpmyadmin",
	Example: "  install -s php",
	Run: func(cmd *cobra.Command, args []string) {
		ls := []*input.InstallParams{
			&input.InstallParams{
				Key:     "php",
				Version: "7.4",
			}, &input.InstallParams{
				Key:     "webserver",
				Version: "1.24.0",
			}, &input.InstallParams{
				Key:     "phpmyadmin",
				Version: "5.2.1",
			},
		}
		for _, v := range ls {
			fmt.Println("开始安装" + v.Key)
			op, err := software.NewInstallOP(v)
			if err != nil {
				fmt.Println(err.Error())
				return
			}
			fn, err := op.Install(true)
			fmt.Println("开始安装：日志位于:", fn)
			if err != nil {
				fmt.Println("安装失败" + err.Error())
			}
		}
	},
}

var changePortCmd = &cobra.Command{
	Use:     "changePort",
	Short:   "修改端口,修改端口后,需要执行 server restart 才能生效",
	Example: "changePort -p 8080",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetString("port")
		if port == "" {
			log.Println("Use \n" +
				"one changePort -p 8080")
			log.Fatalf("Port not provided")
		}

		err := system.UpdateSystemPort(port)
		if err != nil {
			log.Fatalf("Failed to update system port: %v", err)
		}
	},
}

var debugCmd = &cobra.Command{
	Use:     "debug",
	Short:   "debug",
	Example: "debug",
	Run: func(cmd *cobra.Command, args []string) {
		app.ENV = "debug"
		startServer()
	},
}

var updateCmd = &cobra.Command{
	Use:     "update",
	Short:   "Update system components",
	Example: "update",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("开始系统更新...")

		// 创建临时文件
		tmpFile, err := os.CreateTemp("", "update-*.sh")
		if err != nil {
			log.Fatalf("创建临时文件失败: %v", err)
		}
		defer os.Remove(tmpFile.Name())

		// 下载更新脚本
		fmt.Println("下载更新脚本...")
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get("https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/update.sh")
		if err != nil {
			log.Fatalf("下载失败: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Fatalf("服务器返回错误状态码: %d", resp.StatusCode)
		}

		// 保存到临时文件
		if _, err := io.Copy(tmpFile, resp.Body); err != nil {
			log.Fatalf("保存脚本失败: %v", err)
		}

		// 设置执行权限
		if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
			log.Fatalf("设置执行权限失败: %v", err)
		}

		// 创建日志文件
		logFile := "update_" + time.Now().Format("20060102-150405") + ".log"
		f, err := os.Create(logFile)
		if err != nil {
			log.Fatalf("创建日志文件失败: %v", err)
		}
		defer f.Close()

		// 执行更新脚本（同时输出到文件和控制台）
		fmt.Printf("执行更新脚本，日志保存至: %s\n", logFile)
		updateCmd := exec.Command("bash", tmpFile.Name())

		// 创建多路输出器
		multiStdout := io.MultiWriter(f, os.Stdout)
		multiStderr := io.MultiWriter(f, os.Stderr)

		updateCmd.Stdout = multiStdout
		updateCmd.Stderr = multiStderr

		if err := updateCmd.Run(); err != nil {
			log.Fatalf("\n更新执行失败: %v\n请查看完整日志文件: %s", err, logFile)
		}

		fmt.Println("\n系统更新完成！")
	},
}
