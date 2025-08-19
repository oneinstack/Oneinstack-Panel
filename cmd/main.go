package main

import (
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/services/software"
	web "oneinstack/router"
	"oneinstack/router/input"
	"oneinstack/server"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// 版本信息变量（通过ldflags注入）
var (
	Version    = "dev"
	BuildTime  = "unknown"
	CommitHash = "unknown"
	WebVersion = "dev"
)

var userName string
var password string
var initialized bool // 记录是否已经初始化

func main() {
	// 检查是否是version命令，如果是则不启动服务器
	if len(os.Args) > 1 && os.Args[1] == "version" {
		// 直接显示版本信息，不进行任何初始化
		fmt.Printf("Oneinstack Panel\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		fmt.Printf("Commit Hash: %s\n", CommitHash)
		return
	}

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
	rootCmd.AddCommand(versionCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "one",
	Short: "oneinstack",
}

// versionCmd 显示版本信息
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("Oneinstack Panel\n")
		fmt.Printf("Version: %s\n", Version)
		fmt.Printf("Build Time: %s\n", BuildTime)
		fmt.Printf("Commit Hash: %s\n", CommitHash)
	},
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

// 启动服务器
func startServer() {
	// 检查是否已经在运行
	if isServerRunning() {
		fmt.Println("Server is already running.")
		return
	}

	// 启动服务器
	go func() {
		web.SetupRouter().Run(":8089")
	}()

	// 保存PID
	savePID()

	fmt.Println("Server started successfully on port 8089")
}

// 重启服务器
func restartServer() {
	stopServer()
	time.Sleep(2 * time.Second) // 等待服务器完全停止
	startServer()
}

// 停止服务器
func stopServer() {
	if !isServerRunning() {
		fmt.Println("Server is not running.")
		return
	}

	// 读取PID
	pid := readPID()
	if pid == 0 {
		fmt.Println("Cannot read PID file.")
		return
	}

	// 发送终止信号
	process, err := os.FindProcess(pid)
	if err != nil {
		fmt.Printf("Cannot find process with PID %d: %v\n", pid, err)
		return
	}

	err = process.Signal(syscall.SIGTERM)
	if err != nil {
		fmt.Printf("Cannot send signal to process %d: %v\n", pid, err)
		return
	}

	// 等待进程结束
	time.Sleep(2 * time.Second)

	// 检查进程是否还在运行
	if isProcessRunning(pid) {
		// 强制终止
		err = process.Signal(syscall.SIGKILL)
		if err != nil {
			fmt.Printf("Cannot kill process %d: %v\n", pid, err)
			return
		}
	}

	// 删除PID文件
	removePID()

	fmt.Println("Server stopped successfully.")
}

// 检查服务器是否在运行
func isServerRunning() bool {
	pid := readPID()
	if pid == 0 {
		return false
	}
	return isProcessRunning(pid)
}

// 检查进程是否在运行
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// 发送信号0来检查进程是否存在
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// 保存PID到文件
func savePID() {
	pid := os.Getpid()
	err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d", pid)), 0644)
	if err != nil {
		log.Printf("Cannot write PID file: %v", err)
	}
}

// 从文件读取PID
func readPID() int {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}

	return pid
}

// 删除PID文件
func removePID() {
	os.Remove(pidFile)
}

// 定义 resetPwdCmd 指令
var resetPwdCmd = &cobra.Command{
	Use:     "resetpwd",
	Short:   "Reset user password",
	Example: "go run main.go resetpwd --user=admin --password=newpassword",
	Run: func(cmd *cobra.Command, args []string) {
		userName, _ := cmd.Flags().GetString("user")
		password, _ := cmd.Flags().GetString("password")
		if userName == "" || password == "" {
			fmt.Println("Please provide both username and password")
			return
		}
		// 重置密码
		fmt.Printf("Resetting password for user: %s\n", userName)
		// TODO: 实现密码重置功能
	},
}

// 定义 resetUserCmd 指令
var resetUserCmd = &cobra.Command{
	Use:     "resetuser",
	Short:   "Reset username",
	Example: "go run main.go resetuser --user=newusername",
	Run: func(cmd *cobra.Command, args []string) {
		userName, _ := cmd.Flags().GetString("user")
		if userName == "" {
			fmt.Println("Please provide username")
			return
		}
		// 重置用户名
		fmt.Printf("Resetting username to: %s\n", userName)
		// TODO: 实现用户名重置功能
	},
}

// 定义 changePortCmd 指令
var changePortCmd = &cobra.Command{
	Use:     "changeport",
	Short:   "Change system port",
	Example: "go run main.go changeport --port=8089",
	Run: func(cmd *cobra.Command, args []string) {
		port, _ := cmd.Flags().GetString("port")
		if port == "" {
			fmt.Println("Please provide port")
			return
		}
		// 更改端口
		fmt.Printf("Changing port to: %s\n", port)
		// TODO: 实现端口更改功能
	},
}

// 定义 debugCmd 指令
var debugCmd = &cobra.Command{
	Use:     "debug",
	Short:   "Start debug mode",
	Example: "go run main.go debug",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Starting debug mode...")
		// 启动调试模式
		web.SetupRouter().Run(":8089")
	},
}

// 定义 updateCmd 指令
var updateCmd = &cobra.Command{
	Use:     "update",
	Short:   "Update system",
	Example: "go run main.go update",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Updating system...")
		// 更新系统
		fmt.Println("System update functionality not implemented yet")
		// TODO: 实现系统更新功能
	},
}

// 定义 install 指令
var install = &cobra.Command{
	Use:     "install",
	Short:   "Install software",
	Example: "go run main.go install",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) < 1 {
			fmt.Println("Please provide software name")
			return
		}
		softwareName := args[0]
		fmt.Printf("Installing %s...\n", softwareName)

		// 创建安装参数
		params := &input.InstallParams{
			Key:     softwareName,
			Version: "latest",
		}

		// 执行安装
		installer := software.NewInstaller()
		logFileName, err := installer.Install(params, false) // 异步安装
		if err != nil {
			fmt.Printf("Installation failed: %v\n", err)
			return
		}

		fmt.Printf("Installation started. Log file: %s\n", logFileName)
		fmt.Println("You can monitor the installation progress using the web interface.")
	},
}
