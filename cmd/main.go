package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"oneinstack/app"
	"oneinstack/internal/buildinfo"
	"oneinstack/internal/services/audit"
	"oneinstack/internal/services/certificate"
	"oneinstack/internal/services/databasetask"
	"oneinstack/internal/services/filemanager"
	runtimelog "oneinstack/internal/services/log"
	"oneinstack/internal/services/monitoring"
	"oneinstack/internal/services/panelupdate"
	"oneinstack/internal/services/software"
	"oneinstack/internal/services/softwaretask"
	systemservice "oneinstack/internal/services/system"
	"oneinstack/internal/services/websitetask"
	web "oneinstack/router"
	cronHandler "oneinstack/router/handler/cron"
	softwareHandler "oneinstack/router/handler/software"
	storageHandler "oneinstack/router/handler/storage"
	websiteHandler "oneinstack/router/handler/website"
	"oneinstack/router/input"
	panelServer "oneinstack/server"
	"oneinstack/utils"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var userName string
var password string
var passwordFile string

func main() {
	//初始化服务
	resetPwdCmd.Flags().StringP("user", "u", "", "username")
	resetPwdCmd.Flags().StringP("password", "p", "", "new password")

	resetUserCmd.Flags().StringP("user", "u", "", "new username")

	changePortCmd.Flags().StringP("port", "p", "", "New port for the system")
	configureUpdateCommands()

	// 绑定 --user 和 --password 参数到 init 命令
	initCmd.Flags().StringVarP(&userName, "user", "u", "", "Specify the username")
	initCmd.Flags().StringVarP(&password, "password", "p", "", "Specify the password (deprecated; use --password-file)")
	initCmd.Flags().StringVar(&passwordFile, "password-file", "", "Read the password from a file")

	// 用户名必填；密码必须通过互斥的 --password 或 --password-file 提供。
	initCmd.MarkFlagRequired("user")

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
	rootCmd.AddCommand(panelEntryCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "one",
	Short: "oneinstack",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd == versionCmd || isUpdateCommand(cmd) {
			return nil
		}
		if cmd == debugCmd {
			app.ENV = "debug"
			if strings.TrimSpace(os.Getenv("ONEINSTACK_BASE_PATH")) == "" {
				cacheDir, err := os.UserCacheDir()
				if err != nil {
					return fmt.Errorf("resolve debug data directory: %w", err)
				}
				app.BASE_PATH = filepath.Join(
					cacheDir,
					"oneinstack-panel",
					"debug",
				) + string(os.PathSeparator)
			}
		}
		if err := app.Initialize(); err != nil {
			return fmt.Errorf("initialize application: %w", err)
		}
		return nil
	},
}

// versionCmd 显示版本信息
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run: func(cmd *cobra.Command, args []string) {
		info := buildinfo.Current()
		fmt.Printf("Oneinstack Panel\n")
		fmt.Printf("Version: %s\n", info.Version)
		fmt.Printf("Build Time: %s\n", info.BuildTime)
		fmt.Printf("Commit Hash: %s\n", info.CommitHash)
		fmt.Printf("Web Version: %s\n", info.WebVersion)
		fmt.Printf("Go Version: %s\n", info.GoVersion)
		fmt.Printf("Platform: %s/%s\n", info.OS, info.Arch)
	},
}

var panelEntryCmd = &cobra.Command{
	Use:   systemservice.PanelEntryCLISubcommand,
	Short: "Show the current panel access entry",
	RunE: func(cmd *cobra.Command, args []string) error {
		settings, err := systemservice.GetPanelNetworkSettings()
		if err != nil {
			return err
		}
		fmt.Print(formatPanelEntryOutput(settings))
		return nil
	},
}

func formatPanelEntryOutput(settings *systemservice.PanelNetworkSettings) string {
	if settings == nil {
		return ""
	}
	if settings.PanelEntryEnabled {
		return fmt.Sprintf("安全入口已开启，请使用以下地址访问面板：\n%s\n", settings.PanelAccessURL)
	}
	output := fmt.Sprintf("安全入口未开启，当前面板访问地址：\n%s\n", settings.HTTPAccessURL)
	if settings.HTTPSEnabled {
		output += fmt.Sprintf("HTTPS 地址：\n%s\n", settings.HTTPSAccessURL)
	}
	return output
}

// 定义 initCmd 指令
var initCmd = &cobra.Command{
	Use:     "init",
	Short:   "Initialize the system with a username and password",
	Example: "one init --user=admin --password-file=/run/secrets/one-admin-password",
	RunE: func(cmd *cobra.Command, args []string) error {
		hasUsers, err := app.HasUsers()
		if err != nil {
			return err
		}
		if hasUsers {
			fmt.Println("管理员用户已经存在，跳过初始化。")
			return nil
		}

		resolvedPassword, err := resolveInitPassword(password, passwordFile)
		if err != nil {
			return err
		}
		return app.InitUser(userName, resolvedPassword)
	},
}

func resolveInitPassword(flagValue, fileName string) (string, error) {
	if flagValue != "" && fileName != "" {
		return "", fmt.Errorf("--password and --password-file cannot be used together")
	}
	if fileName == "" {
		if flagValue == "" {
			return "", fmt.Errorf("one of --password or --password-file is required")
		}
		return flagValue, nil
	}

	file, err := os.Open(fileName)
	if err != nil {
		return "", fmt.Errorf("open password file: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("stat password file: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		return "", fmt.Errorf("password file must be a regular file")
	}
	if fileInfo.Mode().Perm()&0077 != 0 {
		return "", fmt.Errorf("password file must not be accessible by group or other users")
	}

	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	if len(contents) > 4096 {
		return "", fmt.Errorf("password file is too large")
	}
	resolved := strings.TrimRight(string(contents), "\r\n")
	if resolved == "" {
		return "", fmt.Errorf("password file is empty")
	}
	return resolved, nil
}

// serverStopCmd 定义启动和停止服务的命令
var serverCmd = &cobra.Command{
	Use:     "server",
	Short:   "Start, restart, or stop HTTP server",
	Example: "go run main.go server [start|restart|stop]",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "start":
			return startServer()
		case "restart":
			return restartServer()
		case "stop":
			return stopServer()
		default:
			return fmt.Errorf("invalid action %q: use start, restart, or stop", args[0])
		}
	},
}

// 启动服务器
func startServer() error {
	// 检查是否已经在运行
	if isServerRunning() {
		return fmt.Errorf("server is already running")
	}

	networkConfig := panelServer.PanelConfig{
		BindAddress:     app.ONE_CONFIG.System.BindAddress,
		HTTPPort:        app.ONE_CONFIG.System.Port,
		HTTPSEnabled:    app.ONE_CONFIG.System.HTTPSEnabled,
		HTTPSPort:       app.ONE_CONFIG.System.HTTPSPort,
		CertificateFile: app.ONE_CONFIG.System.HTTPSCertificateFile,
		PrivateKeyFile:  app.ONE_CONFIG.System.HTTPSPrivateKeyFile,
		TrustedProxies:  app.ONE_CONFIG.System.TrustedProxies,
	}
	if err := panelServer.ValidatePanelConfig(networkConfig); err != nil {
		return err
	}

	runtimeLogManager, err := runtimelog.NewRuntimeManager(
		app.DB(),
		app.ONE_CONFIG.System.RuntimeLogRetentionDays,
		app.ONE_CONFIG.System.RuntimeLogCleanupSchedule,
	)
	if err != nil {
		return fmt.Errorf("initialize runtime log service: %w", err)
	}
	if err := runtimeLogManager.Start(); err != nil {
		return fmt.Errorf("start runtime log service: %w", err)
	}
	runtimelog.ConfigureRuntimeDefault(runtimeLogManager)
	previousLogWriter := log.Writer()
	log.SetOutput(io.MultiWriter(previousLogWriter, runtimeLogManager.Writer("panel")))
	defer func() {
		log.SetOutput(previousLogWriter)
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := runtimeLogManager.Stop(stopContext); stopErr != nil {
			fmt.Fprintf(os.Stderr, "stop runtime log service: %v\n", stopErr)
		}
		runtimelog.ClearRuntimeDefault(runtimeLogManager)
	}()

	auditKey, err := utils.DeriveCredentialSubkey("audit-log-hmac-v1")
	if err != nil {
		return fmt.Errorf("derive audit signing key: %w", err)
	}
	auditManager, err := audit.ConfigureDefault(app.DB(), auditKey)
	if err != nil {
		return fmt.Errorf("initialize audit service: %w", err)
	}
	defer audit.ClearDefault(auditManager)
	auditCleaner, err := audit.NewCleaner(
		auditManager,
		app.ONE_CONFIG.System.AuditRetentionDays,
		app.ONE_CONFIG.System.AuditCleanupSchedule,
	)
	if err != nil {
		return err
	}
	auditCleaner.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := auditCleaner.Stop(stopContext); stopErr != nil {
			log.Printf("stop audit cleaner: %v", stopErr)
		}
	}()

	monitorManager, err := monitoring.NewManager(
		app.DB(),
		monitoring.NewSystemCollector(),
		monitoring.NewWebhookSender(),
		app.ONE_CONFIG.System.MonitorRetentionDays,
		app.ONE_CONFIG.System.MonitorAlertRetentionDays,
		app.ONE_CONFIG.System.MonitorSampleSchedule,
		app.ONE_CONFIG.System.MonitorCleanupSchedule,
	)
	if err != nil {
		return fmt.Errorf("initialize monitoring service: %w", err)
	}
	monitoring.ConfigureDefault(monitorManager)
	monitorManager.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := monitorManager.Stop(stopContext); stopErr != nil {
			log.Printf("stop monitoring service: %v", stopErr)
		}
		monitoring.ClearDefault(monitorManager)
	}()

	if err := cronHandler.InitializeService(); err != nil {
		return fmt.Errorf("initialize cron service: %w", err)
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := cronHandler.StopService(stopContext); stopErr != nil {
			log.Printf("stop cron service: %v", stopErr)
		}
	}()
	trashCleaner, err := filemanager.NewTrashCleaner(
		app.ONE_CONFIG.System.DefaultPath,
		app.ONE_CONFIG.System.TrashRetentionDays,
		app.ONE_CONFIG.System.TrashCleanupSchedule,
	)
	if err != nil {
		return err
	}
	trashCleaner.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := trashCleaner.Stop(stopContext); stopErr != nil {
			log.Printf("stop trash cleaner: %v", stopErr)
		}
	}()

	taskManager, err := softwareHandler.DefaultTaskManager()
	if err != nil {
		return fmt.Errorf("initialize software task manager: %w", err)
	}
	taskCleaner, err := softwaretask.NewCleaner(
		taskManager,
		app.ONE_CONFIG.System.SoftwareTaskRetentionDays,
		app.ONE_CONFIG.System.SoftwareTaskLogRetentionDays,
		app.ONE_CONFIG.System.SoftwareTaskCleanupSchedule,
	)
	if err != nil {
		return err
	}
	taskCleaner.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := taskCleaner.Stop(stopContext); stopErr != nil {
			log.Printf("stop software task cleaner: %v", stopErr)
		}
	}()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := taskManager.Stop(stopContext); stopErr != nil {
			log.Printf("stop software task manager: %v", stopErr)
		}
	}()

	databaseManager, err := storageHandler.DefaultDatabaseTaskManager()
	if err != nil {
		return fmt.Errorf("initialize database task manager: %w", err)
	}
	databaseCleaner, err := databasetask.NewCleaner(
		databaseManager,
		app.ONE_CONFIG.System.DatabaseBackupRetentionDays,
		app.ONE_CONFIG.System.DatabaseBackupCleanupSchedule,
	)
	if err != nil {
		return err
	}
	databaseCleaner.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := databaseCleaner.Stop(stopContext); stopErr != nil {
			log.Printf("stop database backup cleaner: %v", stopErr)
		}
	}()

	websiteTaskManager, err := websiteHandler.DefaultWebsiteTaskManager()
	if err != nil {
		return fmt.Errorf("initialize website task manager: %w", err)
	}
	websiteBackupCleaner, err := websitetask.NewCleaner(
		websiteTaskManager,
		app.ONE_CONFIG.System.WebsiteBackupRetentionDays,
		app.ONE_CONFIG.System.WebsiteBackupCleanupSchedule,
	)
	if err != nil {
		return err
	}
	websiteBackupCleaner.Start()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := websiteBackupCleaner.Stop(stopContext); stopErr != nil {
			log.Printf("stop website backup cleaner: %v", stopErr)
		}
	}()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := websiteTaskManager.Stop(stopContext); stopErr != nil {
			log.Printf("stop website task manager: %v", stopErr)
		}
	}()

	certificateManager, err := websiteHandler.DefaultCertificateManager()
	if err != nil {
		return fmt.Errorf("initialize certificate task manager: %w", err)
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := certificateManager.Stop(stopContext); stopErr != nil {
			log.Printf("stop certificate task manager: %v", stopErr)
		}
	}()
	certificateRenewal, err := certificate.NewRenewalScheduler(
		certificateManager,
		app.ONE_CONFIG.System.ACMERenewSchedule,
	)
	if err != nil {
		return err
	}
	if err := certificateRenewal.Start(); err != nil {
		return err
	}
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stopErr := certificateRenewal.Stop(stopContext); stopErr != nil {
			log.Printf("stop certificate renewal scheduler: %v", stopErr)
		}
	}()
	defer func() {
		stopContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if stopErr := databaseManager.Stop(stopContext); stopErr != nil {
			log.Printf("stop database task manager: %v", stopErr)
		}
	}()

	if err := savePID(); err != nil {
		return err
	}
	defer removePID()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpAddress, _ := panelServer.NetworkAddress(networkConfig.BindAddress, networkConfig.HTTPPort)
	log.Printf("OneinStack Panel HTTP listening on %s", httpAddress)
	if networkConfig.HTTPSEnabled {
		httpsAddress, _ := panelServer.NetworkAddress(networkConfig.BindAddress, networkConfig.HTTPSPort)
		log.Printf("OneinStack Panel HTTPS listening on %s", httpsAddress)
	}
	if err := panelServer.RunPanel(ctx, networkConfig, web.SetupRouter()); err != nil {
		return err
	}
	log.Printf("OneinStack Panel stopped")
	return nil
}

// 重启服务器
func restartServer() error {
	if err := stopServer(); err != nil {
		return err
	}
	return startServer()
}

// 停止服务器
func stopServer() error {
	if !isServerRunning() {
		fmt.Println("Server is not running.")
		removePID()
		return nil
	}

	// 读取PID
	pid := readPID()
	if pid == 0 {
		return fmt.Errorf("cannot read PID file")
	}

	// 发送终止信号
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := process.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop process %d: %w", pid, err)
	}

	if !waitForProcessExit(pid, 10*time.Second) {
		return fmt.Errorf("process %d did not stop within 10 seconds", pid)
	}

	removePID()
	fmt.Println("Server stopped successfully.")
	return nil
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

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !isProcessRunning(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !isProcessRunning(pid)
}

func pidFilePath() string {
	return filepath.Join(app.GetBasePath(), "server.pid")
}

// 保存PID到文件
func savePID() error {
	pid := os.Getpid()
	if err := os.WriteFile(pidFilePath(), []byte(fmt.Sprintf("%d", pid)), 0600); err != nil {
		return fmt.Errorf("write PID file: %w", err)
	}
	return nil
}

// 从文件读取PID
func readPID() int {
	data, err := os.ReadFile(pidFilePath())
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
	if err := os.Remove(pidFilePath()); err != nil && !os.IsNotExist(err) {
		log.Printf("Cannot remove PID file: %v", err)
	}
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
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("Starting debug mode...")
		return startServer()
	},
}

var updateManifestURL string
var updateConfirmed bool

// updateCmd manages signed, transactional panel updates. The actual apply
// process is normally launched by the separate one-update.service unit so it
// survives stopping one.service.
var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check, apply, inspect, or roll back signed panel updates",
}

var updateCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "Verify the signed manifest and check for a newer release",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := panelupdate.LoadApplicationManager(updateManifestURL)
		if err != nil {
			return err
		}
		result, err := manager.Check(cmd.Context())
		if err != nil {
			return err
		}
		return printJSON(result)
	},
}

var updateApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply the newest compatible signed release transactionally",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !updateConfirmed {
			return fmt.Errorf("update apply requires --yes")
		}
		manager, err := panelupdate.LoadApplicationManager(updateManifestURL)
		if err != nil {
			return err
		}
		status, err := manager.Apply(cmd.Context())
		if printErr := printJSON(status); printErr != nil && err == nil {
			return printErr
		}
		return err
	},
}

var updateStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the latest durable panel update status",
	RunE: func(cmd *cobra.Command, args []string) error {
		manager, err := panelupdate.LoadApplicationManager("")
		if err != nil {
			return err
		}
		status, err := manager.Status()
		if err != nil {
			return err
		}
		return printJSON(status)
	},
}

var updateRollbackCmd = &cobra.Command{
	Use:   "rollback",
	Short: "Recover the previous release from an interrupted update transaction",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !updateConfirmed {
			return fmt.Errorf("update rollback requires --yes")
		}
		manager, err := panelupdate.LoadApplicationManager("")
		if err != nil {
			return err
		}
		status, err := manager.RollbackLast(cmd.Context())
		if printErr := printJSON(status); printErr != nil && err == nil {
			return printErr
		}
		return err
	},
}

var updatePreflightCmd = &cobra.Command{
	Use:    "preflight",
	Short:  "Run embedded database migrations against an isolated database copy",
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := app.Initialize(); err != nil {
			return fmt.Errorf("migration preflight failed: %w", err)
		}
		fmt.Println("migration preflight passed")
		return nil
	},
}

func configureUpdateCommands() {
	updateCheckCmd.Flags().StringVar(&updateManifestURL, "manifest-url", "", "Override configured signed manifest URL")
	updateApplyCmd.Flags().StringVar(&updateManifestURL, "manifest-url", "", "Override configured signed manifest URL")
	updateApplyCmd.Flags().BoolVar(&updateConfirmed, "yes", false, "Confirm applying the panel update")
	updateRollbackCmd.Flags().BoolVar(&updateConfirmed, "yes", false, "Confirm rollback to the previous release")
	updateCmd.AddCommand(updateCheckCmd, updateApplyCmd, updateStatusCmd, updateRollbackCmd, updatePreflightCmd)
}

func isUpdateCommand(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		if current == updateCmd {
			return true
		}
	}
	return false
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
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
