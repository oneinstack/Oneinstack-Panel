package web

import (
	"fmt"
	"io"
	"oneinstack/app"
	authzHandler "oneinstack/router/handler/access"
	approvalHandler "oneinstack/router/handler/approval"
	"time"

	logservice "oneinstack/internal/services/log"
	auditHandler "oneinstack/router/handler/audit"
	"oneinstack/router/handler/cron"
	"oneinstack/router/handler/ftp"
	"oneinstack/router/handler/health"
	logHandler "oneinstack/router/handler/log"
	monitoringHandler "oneinstack/router/handler/monitoring"
	"oneinstack/router/handler/safe"
	securityHandler "oneinstack/router/handler/security"
	"oneinstack/router/handler/software"
	"oneinstack/router/handler/ssh"
	"oneinstack/router/handler/storage"
	"oneinstack/router/handler/system"
	"oneinstack/router/handler/user"
	"oneinstack/router/handler/website"
	"oneinstack/router/middleware"

	"github.com/gin-gonic/gin"
)

func SetupRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	logOutput := io.Writer(gin.DefaultWriter)
	if manager := logservice.RuntimeDefault(); manager != nil {
		logOutput = io.MultiWriter(gin.DefaultWriter, manager.Writer("http"))
	}
	r := gin.New()
	trustedProxies := app.ONE_CONFIG.System.TrustedProxies
	if len(trustedProxies) == 0 {
		_ = r.SetTrustedProxies(nil)
	} else {
		_ = r.SetTrustedProxies(trustedProxies)
	}
	r.Use(middleware.SecurityHeaders())
	r.Use(gin.LoggerWithConfig(gin.LoggerConfig{
		Output: logOutput,
		SkipPaths: []string{
			"/health/live",
			"/health/ready",
			"/v1/log/runtime",
			"/v1/log/runtime/stats",
			"/v1/log/runtime/stream",
		},
		Formatter: func(parameters gin.LogFormatterParams) string {
			return fmt.Sprintf(
				"[HTTP] %s %s %d %s %s\n",
				parameters.Method,
				parameters.Path,
				parameters.StatusCode,
				parameters.Latency.Round(time.Millisecond),
				parameters.ClientIP,
			)
		},
	}), gin.Recovery())

	r.NoRoute(middleware.MidUiHandle)
	r.GET("/health/live", health.Live)
	r.GET("/health/ready", health.Ready)
	api := r.Group("/v1")

	// 公共路由必须显式注册在受保护路由组之外。
	{
		api.POST("/login",
			middleware.PanelEntryGuard(),
			middleware.AuditLog(),
			middleware.LoginRateLimitMiddleware(),
			user.LoginHandler,
		)
		api.GET("/sys/getbaseinfo", middleware.PanelEntryGuard(), system.GetInfo)
	}

	// 除上述白名单外，所有 API 默认要求认证、限流并记录审计日志。
	protected := api.Group("")
	protected.Use(middleware.AuditLog())
	protected.Use(middleware.APIRateLimitMiddleware())
	protected.Use(middleware.AuthMiddleware())
	protected.Use(middleware.LoadAuthorizationContext())
	protected.Use(middleware.CSRFMiddleware())
	protected.Use(middleware.RequirePasswordChange())
	protected.POST("/logout", user.LogoutHandler)
	protected.GET("/auth/me", authzHandler.Me)
	protected.GET("/access/matrix", authzHandler.Matrix)
	protected.GET("/sessions", securityHandler.ListSessions)
	protected.POST("/sessions/revoke-others", securityHandler.RevokeOtherSessions)
	protected.POST("/sessions/:id/revoke", securityHandler.RevokeSession)
	securityg := protected.Group("/security")
	{
		securityg.GET("/status", securityHandler.Status)
		securityg.POST("/totp/setup", securityHandler.SetupTOTP)
		securityg.POST("/totp/confirm", securityHandler.ConfirmTOTP)
		securityg.POST("/totp/disable", securityHandler.DisableTOTP)
		securityg.POST("/totp/recovery-codes/regenerate", securityHandler.RegenerateRecoveryCodes)
	}

	accessg := protected.Group("/access")
	accessg.Use(middleware.RequireSuperAdmin())
	{
		accessg.GET("/users", authzHandler.ListUsers)
		accessg.GET("/roles", authzHandler.ListRoles)
		accessg.POST("/users", authzHandler.CreateUser)
		accessg.PUT("/users/:id/roles", authzHandler.AssignRoles)
		accessg.POST("/users/:id/reset-password", authzHandler.ResetUserPassword)
	}

	// 系统接口
	sys := protected.Group("/sys")
	{
		sys.GET("/info", system.GetSystemInfo)
		sys.GET("/version", health.Version)
		sys.GET("/monitor", system.GetSystemMonitor)
		sys.GET("/libcount", system.GetLibCount)
		sys.GET("/websitecount", system.GetWebSiteCount)
		sys.GET("/remarkcount", system.GetRemarkCount)
		sys.GET("/systeminfo", system.SystemInfo)
		sys.POST("/updateuser", system.UpdateUser)
		sys.POST("/resetpassword", system.ResetPassword)
		sys.POST("/updateport", middleware.RequireAdmin(), system.UpdatePort)
		sys.GET("/network", middleware.RequireAdmin(), system.GetPanelNetwork)
		sys.POST("/network", middleware.RequireAdmin(), system.UpdatePanelNetwork)
		sys.POST("/updatesystemtitle", system.UpdateSystemTitle)

		//备注相关
		sys.GET("/remark/:id", system.RemarkList)
		sys.GET("/remark", system.RemarkList)
		sys.POST("/remark/add", system.AddRemark)
		sys.POST("/remark/update", system.UpdateRemark)
		sys.POST("/remark/del", system.DeleteRemark)

		sys.POST("/dic/list", system.DictionaryList)
		sys.POST("/dic/add", system.AddDictionary)
		sys.POST("/dic/update", system.UpdateDictionary)
		sys.POST("/dic/del", system.DeleteDictionary)
	}
	panelUpdate := sys.Group("/update")
	panelUpdate.Use(middleware.RequireAdmin())
	{
		panelUpdate.GET("/status", system.GetPanelUpdateStatus)
		panelUpdate.POST("/check", system.CheckPanelUpdate)
		panelUpdate.POST("/apply", system.ApplyPanelUpdate)
	}

	// 数据库相关
	storageg := protected.Group("/storage")
	{
		storageg.POST("/addconn", middleware.RequirePermission("database.write"), storage.ADDStorage)
		storageg.POST("/testconn", middleware.RequirePermission("database.write"), storage.TestStorageConnection)
		storageg.POST("/addlib", middleware.RequirePermission("database.write"), storage.ADDLib)
		storageg.POST("/dellib", middleware.RequirePermission("database.write"), storage.DeleteLibrary)
		storageg.POST("/libraries/:id/credential/reveal",
			middleware.RequirePermission("database.write"), storage.RevealLibraryCredential)
		storageg.POST("/libraries/:id/credential/update",
			middleware.RequirePermission("database.write"), storage.UpdateLibraryCredential)
		storageg.POST("/updateconn", middleware.RequirePermission("database.write"), storage.UpdateStorage)
		storageg.GET("/connlist", middleware.RequirePermission("database.read"), storage.GetStorage)
		storageg.POST("/delconn", middleware.RequirePermission("database.write"), storage.DelStorage)
		storageg.POST("/sync", middleware.RequirePermission("database.write"), storage.SyncStorage)
		storageg.POST("/liblist", middleware.RequirePermission("database.read"), storage.GetLib)
		storageg.POST("/rklist", middleware.RequirePermission("database.read"), storage.GetRedisKeys)
		storageg.POST("/info", middleware.RequirePermission("database.read"), storage.Info)
		storageg.POST("/backups", middleware.RequirePermission("database.write"), storage.CreateDatabaseBackup)
		storageg.GET("/backups", middleware.RequirePermission("database.read"), storage.ListDatabaseBackups)
		storageg.GET("/backups/:id/download", middleware.RequirePermission("database.read"), storage.DownloadDatabaseBackup)
		storageg.POST("/backups/:id/delete", middleware.RequirePermission("database.write"), storage.DeleteDatabaseBackup)
		storageg.POST("/restores", middleware.RequirePermission("database.write"), storage.RestoreDatabaseBackup)
		storageg.GET("/tasks", middleware.RequirePermission("database.read"), storage.ListDatabaseTasks)
		storageg.GET("/tasks/:id", middleware.RequirePermission("database.read"), storage.GetDatabaseTask)
		storageg.GET("/tasks/:id/log", middleware.RequirePermission("database.read"), storage.GetDatabaseTaskLog)
		storageg.POST("/tasks/:id/cancel", middleware.RequirePermission("database.write"), storage.CancelDatabaseTask)
	}

	// FTP/文件相关
	ftpg := protected.Group("/ftp")
	{
		ftpg.POST("/list", ftp.ListDirectory)
		ftpg.POST("/create", ftp.CreateFileOrDir)
		ftpg.POST("/upload", ftp.UploadFile)
		ftpg.POST("/download", ftp.DownloadFile)
		ftpg.POST("/urldownload", ftp.UrlDownloadFile)
		ftpg.POST("/content", ftp.Content)
		ftpg.POST("/tree", ftp.GetDirectoryTreeHandler)
		ftpg.POST("/delete", ftp.DeleteFileOrDir)
		ftpg.POST("/modify", ftp.ModifyFileOrDirAttributes)
		ftpg.POST("/save", ftp.SaveFile)
		ftpg.GET("/capacity", ftp.Capacity)
		ftpg.GET("/trash/list", ftp.ListTrash)
		ftpg.POST("/trash/restore", ftp.RestoreTrash)
		ftpg.POST("/trash/delete", ftp.DeleteTrashPermanently)
		ftpg.POST("/trash/empty", ftp.EmptyTrash)
	}

	// 软件相关
	softg := protected.Group("/soft")
	{
		softg.POST("/list", software.GetSoftware)
		softg.GET("/catalog/status", software.GetSoftwareCatalogStatus)
		softg.POST("/catalog/sync", middleware.RequireAdmin(), software.SyncSoftwareCatalog)
		softg.GET("/getlog", software.GetLogContent)
		softg.POST("/install", software.RunInstallation)
		softg.POST("/remove", software.RemoveSoftware)
		softg.POST("/exploration", software.Exploration)
		serviceg := softg.Group("/services")
		serviceg.Use(middleware.RequireAdmin())
		serviceg.GET("", software.ListComponentServices)
		serviceg.GET("/:component", software.GetComponentService)
		serviceg.POST("/:component/actions", software.RunComponentServiceAction)
		serviceg.GET("/:component/config", software.GetComponentServiceConfiguration)
		serviceg.POST("/:component/config/preview", software.PreviewComponentServiceConfiguration)
		serviceg.POST("/:component/config/apply", software.ApplyComponentServiceConfiguration)
		softg.GET("/tasks", software.ListSoftwareTasks)
		softg.GET("/tasks/stats", software.GetSoftwareTaskStats)
		softg.GET("/tasks/:id", software.GetSoftwareTask)
		softg.GET("/tasks/:id/events", software.StreamSoftwareTaskEvents)
		softg.GET("/tasks/:id/log", software.GetSoftwareTaskLog)
		softg.GET("/tasks/:id/log/download", software.DownloadSoftwareTaskLog)
		softg.POST("/tasks/:id/cancel", software.CancelSoftwareTask)
	}

	// 面板运行日志仅管理员可查看。
	logg := protected.Group("/log")
	logg.Use(middleware.RequirePermission("runtime_log.read"))
	{
		logg.GET("/runtime", logHandler.ListRuntimeLogs)
		logg.GET("/runtime/stats", logHandler.RuntimeLogStats)
		logg.GET("/runtime/stream", logHandler.StreamRuntimeLogs)
	}

	auditg := protected.Group("/audit")
	auditg.Use(middleware.RequirePermission("audit.read"))
	{
		auditg.GET("/events", auditHandler.ListEvents)
		auditg.GET("/events/:id", auditHandler.GetEvent)
		auditg.GET("/stats", auditHandler.GetStats)
		auditg.GET("/export", middleware.RequirePermission("audit.export"), auditHandler.ExportEvents)
		auditg.POST("/verify", middleware.RequirePermission("audit.verify"), auditHandler.VerifyChain)
	}

	approvalg := protected.Group("/approvals")
	approvalg.Use(middleware.RequirePermission("approval.read"))
	{
		approvalg.GET("", approvalHandler.List)
		approvalg.GET("/:id", approvalHandler.Get)
		approvalg.POST("/:id/approve", middleware.RequireAnyPermission("approval.review", "approval.execute"), approvalHandler.Approve)
		approvalg.POST("/:id/reject", middleware.RequirePermission("approval.review"), approvalHandler.Reject)
	}

	// 网站相关
	websiteg := protected.Group("/website")
	{
		websiteg.POST("/list", middleware.RequirePermission("website.read"), website.List)
		websiteg.POST("/add", middleware.RequirePermission("website.write"), website.Add)
		websiteg.POST("/del", middleware.RequirePermission("website.write"), website.Delete)
		websiteg.POST("/update", middleware.RequirePermission("website.write"), website.Update)
		websiteg.POST("/info", middleware.RequirePermission("website.read"), website.Info)
		websiteg.POST("/backups", middleware.RequirePermission("website.write"), website.CreateWebsiteBackup)
		websiteg.GET("/backups", middleware.RequirePermission("website.read"), website.ListWebsiteBackups)
		websiteg.GET("/backups/:id/download", middleware.RequirePermission("website.read"), website.DownloadWebsiteBackup)
		websiteg.POST("/backups/:id/delete", middleware.RequirePermission("website.write"), website.DeleteWebsiteBackup)
		websiteg.POST("/restores", middleware.RequirePermission("website.write"), website.RestoreWebsiteBackup)
		websiteg.GET("/tasks", middleware.RequirePermission("website.read"), website.ListWebsiteTasks)
		websiteg.GET("/tasks/:id", middleware.RequirePermission("website.read"), website.GetWebsiteTask)
		websiteg.GET("/tasks/:id/log", middleware.RequirePermission("website.read"), website.GetWebsiteTaskLog)
		websiteg.POST("/tasks/:id/cancel", middleware.RequirePermission("website.write"), website.CancelWebsiteTask)
		websiteg.POST("/certificates/acme", middleware.RequirePermission("website.write"), website.IssueCertificate)
		websiteg.GET("/certificates/:websiteId", middleware.RequirePermission("website.read"), website.GetCertificate)
		websiteg.POST("/certificates/:id/renew", middleware.RequirePermission("website.write"), website.RenewCertificate)
		websiteg.POST("/certificates/:id/disable", middleware.RequirePermission("website.write"), website.DisableCertificate)
		websiteg.GET("/certificate-tasks", middleware.RequirePermission("website.read"), website.ListCertificateTasks)
		websiteg.GET("/certificate-tasks/:id", middleware.RequirePermission("website.read"), website.GetCertificateTask)
		websiteg.GET("/certificate-tasks/:id/log", middleware.RequirePermission("website.read"), website.GetCertificateTaskLog)
		websiteg.POST("/certificate-tasks/:id/cancel", middleware.RequirePermission("website.write"), website.CancelCertificateTask)
	}

	// 安全相关
	safeg := protected.Group("/safe")
	safeg.Use(middleware.RequireAdmin())
	{
		safeg.GET("/info", safe.GetFirewallInfo)
		safeg.POST("/rules", safe.GetFirewallRules)
		safeg.POST("/add", safe.AddFirewallRule)
		safeg.POST("/update", safe.UpdateFirewallRule)
		safeg.POST("/del", safe.DeleteFirewallRule)
		safeg.POST("/stop", safe.StopFirewall)
		safeg.POST("/blockping", safe.BlockPing)
		safeg.POST("/install", safe.InstallFirewall)
	}

	// SSH 相关
	sshg := protected.Group("/ssh")
	{
		sshg.POST("/ticket", ssh.CreateTicket)
		sshg.GET("/open", ssh.OpenSSH)
	}

	// 定时任务相关
	crong := protected.Group("/cron")
	crong.Use(middleware.RequireAdmin())
	{
		crong.GET("/templates", cron.ListTemplates)
		crong.GET("/executions/running", cron.ListRunningExecutions)
		crong.POST("/executions/:id/cancel", cron.CancelExecution)
		crong.POST("/list", cron.GetCronList)
		crong.POST("/add", cron.AddCron)
		crong.POST("/update", cron.UpdateCron)
		crong.POST("/del", cron.DeleteCron)
		crong.POST("/disable", cron.DisableCron)
		crong.POST("/enable", cron.EnableCron)
		crong.POST("/log", cron.GetCronLogList)
		crong.POST("/log/cleanup", cron.CleanupCronLogs)
		crong.GET("/:id/log/export", cron.ExportCronLogs)
		crong.POST("/run", cron.RunCron)
	}

	monitoringg := protected.Group("/monitor")
	monitoringg.Use(middleware.RequireAdmin())
	{
		monitoringg.GET("/summary", monitoringHandler.Summary)
		monitoringg.GET("/metrics", monitoringHandler.Metrics)
		monitoringg.GET("/rules", monitoringHandler.ListRules)
		monitoringg.POST("/rules", monitoringHandler.CreateRule)
		monitoringg.PUT("/rules/:id", monitoringHandler.UpdateRule)
		monitoringg.DELETE("/rules/:id", monitoringHandler.DeleteRule)
		monitoringg.POST("/rules/:id/update", monitoringHandler.UpdateRule)
		monitoringg.POST("/rules/:id/delete", monitoringHandler.DeleteRule)
		monitoringg.POST("/rules/:id/silence", monitoringHandler.SilenceRule)
		monitoringg.GET("/events", monitoringHandler.Events)
		monitoringg.GET("/deliveries", monitoringHandler.Deliveries)
		monitoringg.GET("/channels", monitoringHandler.ListChannels)
		monitoringg.POST("/channels", monitoringHandler.CreateChannel)
		monitoringg.PUT("/channels/:id", monitoringHandler.UpdateChannel)
		monitoringg.DELETE("/channels/:id", monitoringHandler.DeleteChannel)
		monitoringg.POST("/channels/:id/update", monitoringHandler.UpdateChannel)
		monitoringg.POST("/channels/:id/delete", monitoringHandler.DeleteChannel)
		monitoringg.POST("/channels/:id/test", monitoringHandler.TestChannel)
	}

	return r
}
