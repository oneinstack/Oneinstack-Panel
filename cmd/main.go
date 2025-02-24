package main

import (
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/services/software"
	"oneinstack/internal/services/system"
	"oneinstack/internal/services/user"
	web "oneinstack/router"
	"oneinstack/router/input"
	"oneinstack/server"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	server.Start()
	//初始化服务
	resetPwdCmd.Flags().StringP("name", "n", "", "username")
	resetPwdCmd.Flags().StringP("pwd", "p", "", "password")

	resetUserCmd.Flags().StringP("oldn", "", "", "old username")
	resetUserCmd.Flags().StringP("newn", "", "", "new username")

	changePortCmd.Flags().StringP("port", "p", "", "New port for the system")
	// 将命令添加到根命令
	rootCmd.AddCommand(install)
	rootCmd.AddCommand(resetPwdCmd)
	rootCmd.AddCommand(resetUserCmd)
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(changePortCmd)
	rootCmd.AddCommand(debugCmd)

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
	app.InitUser()
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
	Short:   "reset user password",
	Example: " resetpwd -n admin -p 123123 ",
	Run: func(cmd *cobra.Command, args []string) {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			log.Fatalf("username not found")
		}
		pwd, _ := cmd.Flags().GetString("pwd")
		if name == "" {
			log.Fatalf("password not found")
		}
		err := user.ChangePassword(name, pwd)
		if err != nil {
			log.Fatalf("Add user error: %v", err)
		}
	},
}

var resetUserCmd = &cobra.Command{
	Use:     "resetUsername",
	Short:   "reset user username",
	Example: " resetUsername --oldn AHMPotFoxig --newn admin ",
	Run: func(cmd *cobra.Command, args []string) {
		on, _ := cmd.Flags().GetString("oldn")
		if on == "" {
			log.Fatalf("old username not found")
		}
		nn, _ := cmd.Flags().GetString("newn")
		if nn == "" {
			log.Fatalf("new username not found")
		}
		err := user.ResetUsername(on, nn)
		if err != nil {
			log.Fatalf("Add user error: %v", err)
		}
	},
}

var install = &cobra.Command{
	Use:     "install",
	Short:   "安装 php nginx  phpmyadmin",
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
	Use:     "changeport",
	Short:   "修改端口,修改端口后,需要执行 server restart 才能生效",
	Example: "changeport -p 8080",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetString("port")
		if port == "" {
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
