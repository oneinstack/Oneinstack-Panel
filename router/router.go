package web

import (
	"fmt"
	"io"
	"log"
	"oneinstack/app"
	accessservice "oneinstack/internal/services/access"
	translationservice "oneinstack/internal/services/translation"
	authzHandler "oneinstack/router/handler/access"
	approvalHandler "oneinstack/router/handler/approval"
	configsnapshotHandler "oneinstack/router/handler/configsnapshot"
	"time"

	logservice "oneinstack/internal/services/log"
	auditHandler "oneinstack/router/handler/audit"
	bastionHandler "oneinstack/router/handler/bastion"
	certificateHandler "oneinstack/router/handler/certificate"
	containerHandler "oneinstack/router/handler/container"
	"oneinstack/router/handler/cron"
	fail2banHandler "oneinstack/router/handler/fail2ban"
	"oneinstack/router/handler/ftp"
	"oneinstack/router/handler/health"
	logHandler "oneinstack/router/handler/log"
	monitoringHandler "oneinstack/router/handler/monitoring"
	operationpreviewHandler "oneinstack/router/handler/operationpreview"
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
	if app.DB() != nil {
		if err := containerHandler.StartCreateTaskManager(); err != nil {
			log.Printf("container task manager unavailable: %v", err)
		}
		if err := ftp.StartArchiveTaskManager(); err != nil {
			log.Printf("file archive task manager unavailable: %v", err)
		}
	}
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
	r.Use(middleware.Locale())
	translationConfig := app.ONE_CONFIG.Translation
	if translationService, err := translationservice.New(translationConfig, app.DB()); err == nil && translationService != nil {
		r.Use(middleware.ResponseTranslation(
			translationService,
			translationConfig.MaxFieldsPerResponse,
			translationConfig.ResponseTimeoutSeconds,
		))
	} else if translationConfig.Enabled && err != nil {
		log.Printf("translation service initialization deferred: %v", err)
	}
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
		api.GET("/panel-entry/status", system.GetPanelEntryStatus)
		api.GET("/sys/getbaseinfo", middleware.PanelEntryGuard(), system.GetInfo)
		api.GET("/public/file-share/download",
			middleware.RateLimitMiddleware(30, time.Minute),
			ftp.DownloadSharedFile,
		)
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
	protected.POST("/operations/preview", operationpreviewHandler.Preview)
	protected.POST("/operations/:previewId/execute", operationpreviewHandler.Execute)
	certificateg := protected.Group("/certificates")
	{
		certificateg.GET("/algorithms", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ListAlgorithms)
		certificateg.GET("/dns-providers", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ListDNSProviders)
		certificateg.GET("/dns-accounts", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ListDNSAccounts)
		certificateg.POST("/dns-accounts", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.SaveDNSAccount)
		certificateg.DELETE("/dns-accounts/:id", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.DeleteDNSAccount)
		certificateg.POST("/acme", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.IssueACME)
		certificateg.GET("/tasks", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ListTasks)
		certificateg.GET("/tasks/:id", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.GetTask)
		certificateg.GET("/tasks/:id/log", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.GetTaskLog)
		certificateg.POST("/tasks/:id/cancel", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.CancelTask)
		certificateg.GET("", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.List)
		certificateg.POST("/upload", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.Upload)
		certificateg.POST("/self-signed", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.SelfSigned)
		certificateg.GET("/:id/certificate", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ReadCertificate)
		certificateg.GET("/:id/private-key", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.ReadPrivateKey)
		certificateg.GET("/:id/download", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.Download)
		certificateg.GET("/:id", middleware.RequirePermission(accessservice.PermissionCertificateRead), certificateHandler.Get)
		certificateg.POST("/:id/bindings", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.Bind)
		certificateg.DELETE("/:id/bindings/:websiteId", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.Unbind)
		certificateg.DELETE("/:id", middleware.RequirePermission(accessservice.PermissionCertificateWrite), certificateHandler.Delete)
	}
	// 配置快照
	snapshotg := protected.Group("/config-snapshots")
	{
		snapshotg.GET("", middleware.RequirePermission(accessservice.PermissionConfigSnapshotRead), configsnapshotHandler.List)
		snapshotg.POST("", middleware.RequirePermission(accessservice.PermissionConfigSnapshotWrite), configsnapshotHandler.Create)
		snapshotg.GET("/resources/nginx", middleware.RequirePermission(accessservice.PermissionConfigSnapshotRead), configsnapshotHandler.ListNginxResources)
		snapshotg.GET("/:id", middleware.RequirePermission(accessservice.PermissionConfigSnapshotRead), configsnapshotHandler.Get)
		snapshotg.GET("/:id/diff", middleware.RequirePermission(accessservice.PermissionConfigSnapshotRead), configsnapshotHandler.Diff)
		snapshotg.POST("/:id/restore/preview", middleware.RequirePermission(accessservice.PermissionConfigSnapshotRead), configsnapshotHandler.RestorePreview)
		snapshotg.POST("/:id/restore", middleware.RequirePermission(accessservice.PermissionConfigSnapshotWrite), configsnapshotHandler.Restore)
		snapshotg.DELETE("/:id", middleware.RequirePermission(accessservice.PermissionConfigSnapshotWrite), configsnapshotHandler.Delete)
	}
	protected.GET("/auth/me", authzHandler.Me)
	protected.POST("/auth/verify-password", securityHandler.VerifyPassword)
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
		fail2bang := securityg.Group("/fail2ban")
		{
			fail2bang.GET("/status", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Status)
			fail2bang.GET("/templates", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Templates)
			fail2bang.GET("/policies", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Policies)
			fail2bang.GET("/bans", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Bans)
			fail2bang.GET("/incidents", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Incidents)
			fail2bang.POST("/incidents/:id/dismiss", middleware.RequirePermission(accessservice.PermissionSecurityWrite), fail2banHandler.DismissIncident)
			fail2bang.GET("/unban-records", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.UnbanRecords)
			fail2bang.GET("/tasks", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Tasks)
			fail2bang.GET("/tasks/:id", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.Task)
			fail2bang.GET("/tasks/:id/events", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.TaskEvents)
			fail2bang.GET("/tasks/:id/log", middleware.RequirePermission(accessservice.PermissionSecurityRead), fail2banHandler.TaskLog)
		}
	}

	accessg := protected.Group("/access")
	accessg.Use(middleware.RequireSuperAdmin())
	{
		accessg.GET("/users", authzHandler.ListUsers)
		accessg.GET("/roles", authzHandler.ListRoles)
		accessg.GET("/roles/:key", authzHandler.GetRole)
		accessg.POST("/roles", authzHandler.CreateRole)
		accessg.PUT("/roles/:key", authzHandler.UpdateRole)
		accessg.DELETE("/roles/:key", authzHandler.DeleteRole)
		accessg.GET("/permissions", authzHandler.ListPermissions)
		accessg.GET("/menus", authzHandler.ListMenus)
		accessg.POST("/menus", authzHandler.CreateMenu)
		accessg.PUT("/menus/:key", authzHandler.UpdateMenu)
		accessg.DELETE("/menus/:key", authzHandler.DeleteMenu)
		accessg.POST("/users", authzHandler.CreateUser)
		accessg.DELETE("/users/:id", authzHandler.DeleteUser)
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
		sys.GET("/navigation", system.GetNavigationSettings)
		sys.PUT("/navigation", middleware.RequireAdmin(), system.UpdateNavigationSettings)
		sys.GET("/processes", middleware.RequirePermission(accessservice.PermissionSystemRead), system.ListProcesses)
		sys.GET("/processes/:pid", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetProcessDetail)
		sys.GET("/disks", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetDiskInventory)
		sys.GET("/ssh/config", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetSSHConfig)
		sys.POST("/updateuser", system.UpdateUser)
		sys.POST("/resetpassword", system.ResetPassword)
		sys.POST("/updateport", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.UpdatePort)
		sys.GET("/network", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetPanelNetwork)
		sys.POST("/network", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.UpdatePanelNetwork)
		sys.GET("/network/transactions/:id", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetPanelNetworkTransaction)
		sys.POST("/backups", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.CreatePanelBackup)
		sys.POST("/backups/import", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.ImportPanelBackup)
		sys.GET("/backups", middleware.RequirePermission(accessservice.PermissionSystemRead), system.ListPanelBackups)
		sys.GET("/backups/:id/download", middleware.RequirePermission(accessservice.PermissionSystemRead), system.DownloadPanelBackup)
		sys.POST("/backups/:id/delete", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.DeletePanelBackup)
		sys.POST("/backups/:id/preflight", middleware.RequirePermission(accessservice.PermissionSystemRead), system.PreflightPanelBackup)
		sys.POST("/backups/:id/restore", middleware.RequirePermission(accessservice.PermissionSystemWrite), system.RestorePanelBackup)
		sys.GET("/restore/status", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetPanelRestoreStatus)
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
	panelUpdate.Use(middleware.RequirePermission(accessservice.PermissionSystemWrite))
	{
		panelUpdate.GET("/status", middleware.RequirePermission(accessservice.PermissionSystemRead), system.GetPanelUpdateStatus)
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
			middleware.RequirePermission(accessservice.PermissionDatabaseWrite), storage.RevealLibraryCredential)
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
		ftpg.POST("/list", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.ListDirectory)
		ftpg.POST("/search", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.SearchFile)
		ftpg.GET("/operations", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.ListOperations)
		ftpg.POST("/create", middleware.RequirePermission(accessservice.PermissionFileCreate), ftp.CreateFileOrDir)
		ftpg.POST("/upload", middleware.RequirePermission(accessservice.PermissionFileCreate), ftp.UploadFile)
		ftpg.POST("/download", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.DownloadFile)
		ftpg.POST("/preview-ticket", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.CreateImagePreviewTicket)
		ftpg.GET("/preview/:ticket", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.PreviewImage)
		ftpg.POST("/urldownload", middleware.RequirePermission(accessservice.PermissionFileCreate), ftp.UrlDownloadFile)
		ftpg.POST("/content", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.Content)
		ftpg.POST("/tree", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.GetDirectoryTreeHandler)
		ftpg.POST("/delete", middleware.RequirePermission(accessservice.PermissionFileDelete), ftp.DeleteFileOrDir)
		ftpg.POST("/copy", middleware.RequirePermission(accessservice.PermissionFileMove), ftp.CopyFileOrDir)
		ftpg.POST("/move", middleware.RequirePermission(accessservice.PermissionFileMove), ftp.MoveFileOrDir)
		ftpg.POST("/rename", middleware.RequirePermission(accessservice.PermissionFileMove), ftp.RenameFileOrDir)
		ftpg.POST("/modify", middleware.RequirePermission(accessservice.PermissionFileModify), ftp.ModifyFileOrDirAttributes)
		ftpg.POST("/save", middleware.RequirePermission(accessservice.PermissionFileEdit), ftp.SaveFile)
		ftpg.POST("/archive", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.ArchiveFileOrDir)
		ftpg.GET("/archive/tasks", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.ListArchiveTasks)
		ftpg.GET("/archive/tasks/:id", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.GetArchiveTask)
		ftpg.POST("/extract", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.ExtractFile)
		ftpg.GET("/extract/tasks", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.ListExtractTasks)
		ftpg.GET("/extract/tasks/:id", middleware.RequirePermission(accessservice.PermissionFileArchive), ftp.GetExtractTask)
		ftpg.POST("/properties", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.GetFileProperties)
		ftpg.POST("/favorite", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.CreateFavorite)
		ftpg.POST("/favorite/cancel", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.CancelFavorite)
		ftpg.GET("/favorites", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.ListFavorites)
		ftpg.POST("/shares", middleware.RequirePermission(accessservice.PermissionFileShare), ftp.CreateFileShare)
		ftpg.GET("/shares", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.ListFileShares)
		ftpg.POST("/shares/:id/revoke", middleware.RequirePermission(accessservice.PermissionFileShare), ftp.RevokeFileShare)
		ftpg.GET("/capacity", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.Capacity)
		ftpg.GET("/trash/list", middleware.RequirePermission(accessservice.PermissionFileRead), ftp.ListTrash)
		ftpg.POST("/trash/restore", middleware.RequirePermission(accessservice.PermissionFileDelete), ftp.RestoreTrash)
		ftpg.POST("/trash/delete", middleware.RequirePermission(accessservice.PermissionFileDelete), ftp.DeleteTrashPermanently)
		ftpg.POST("/trash/empty", middleware.RequirePermission(accessservice.PermissionFileDelete), ftp.EmptyTrash)
	}

	// 软件相关
	softg := protected.Group("/soft")
	{
		softg.POST("/list", middleware.RequirePermission(accessservice.PermissionSoftwareRead), software.GetSoftware)
		softg.GET("/categories", middleware.RequirePermission(accessservice.PermissionSoftwareRead), software.ListSoftwareCategories)
		softg.GET("/catalog/status", middleware.RequirePermission(accessservice.PermissionSoftwareRead), software.GetSoftwareCatalogStatus)
		softg.POST("/catalog/sync", middleware.RequirePermission(accessservice.PermissionSoftwareWrite), software.SyncSoftwareCatalog)
		softg.GET("/getlog", middleware.RequirePermission(accessservice.PermissionSoftwareRead), software.GetLogContent)
		softg.POST("/install", middleware.RequirePermission(accessservice.PermissionSoftwareWrite), software.RunInstallation)
		softg.POST("/remove", middleware.RequirePermission(accessservice.PermissionSoftwareWrite), software.RemoveSoftware)
		softg.POST("/exploration", middleware.RequirePermission(accessservice.PermissionSoftwareRead), software.Exploration)
		serviceg := softg.Group("/services")
		serviceg.GET("", middleware.RequirePermission(accessservice.PermissionServiceRead), software.ListComponentServices)
		serviceg.GET("/:component", middleware.RequirePermission(accessservice.PermissionServiceRead), software.GetComponentService)
		serviceg.POST("/:component/actions", middleware.RequirePermission(accessservice.PermissionServiceWrite), software.RunComponentServiceAction)
		serviceg.GET("/:component/config", middleware.RequirePermission(accessservice.PermissionServiceRead), software.GetComponentServiceConfiguration)
		serviceg.POST("/:component/config/preview", middleware.RequirePermission(accessservice.PermissionServiceRead), software.PreviewComponentServiceConfiguration)
		serviceg.POST("/:component/config/apply", middleware.RequirePermission(accessservice.PermissionServiceWrite), software.ApplyComponentServiceConfiguration)
		serviceg.GET("/:component/config/history", middleware.RequirePermission(accessservice.PermissionServiceRead), software.ListComponentServiceConfigurationHistory)
		serviceg.POST("/:component/config/history/:id/preview", middleware.RequirePermission(accessservice.PermissionServiceRead), software.PreviewComponentServiceConfigurationRestore)
		serviceg.POST("/:component/config/history/:id/restore", middleware.RequirePermission(accessservice.PermissionServiceWrite), software.RestoreComponentServiceConfiguration)
		softg.GET("/tasks", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.ListSoftwareTasks)
		softg.GET("/tasks/stats", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.GetSoftwareTaskStats)
		softg.GET("/tasks/:id", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.GetSoftwareTask)
		softg.GET("/tasks/:id/events", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.StreamSoftwareTaskEvents)
		softg.GET("/tasks/:id/log", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.GetSoftwareTaskLog)
		softg.GET("/tasks/:id/log/download", middleware.RequirePermission(accessservice.PermissionSoftwareRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll), software.DownloadSoftwareTaskLog)
		softg.POST("/tasks/:id/cancel", middleware.RequirePermission(accessservice.PermissionSoftwareWrite), middleware.RequirePermission(accessservice.PermissionTaskCancelSelf), software.CancelSoftwareTask)
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
		websiteg.POST("/:id/status", middleware.RequirePermission("website.write"), website.SetStatus)
		websiteg.GET("/:id/settings", middleware.RequirePermission("website.read"), website.GetWebsiteSettings)
		websiteg.PUT("/:id/settings", middleware.RequirePermission("website.write"), website.UpdateWebsiteSettings)
		websiteg.GET("/:id/log", middleware.RequirePermission("website.read"), website.GetWebsiteLog)
		websiteg.GET("/:id/config", middleware.RequirePermission("website.read"), website.GetWebsiteManagedConfig)
		websiteg.PUT("/:id/config", middleware.RequirePermission("website.write"), website.UpdateWebsiteManagedConfig)
		websiteg.POST("/info", middleware.RequirePermission("website.read"), website.Info)
		websiteg.GET("/web-server", middleware.RequirePermission("website.read"), website.GetWebServerStatus)
		websiteg.GET("/web-server/configs", middleware.RequirePermission("website.read"), website.ListWebServerConfigs)
		websiteg.GET("/web-server/config", middleware.RequirePermission("website.read"), website.GetWebServerConfig)
		websiteg.PUT("/web-server/config", middleware.RequirePermission("website.write"), website.UpdateWebServerConfig)
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
	{
		safeg.GET("/info", middleware.RequirePermission(accessservice.PermissionSecurityRead), safe.GetFirewallInfo)
		safeg.POST("/rules", middleware.RequirePermission(accessservice.PermissionSecurityRead), safe.GetFirewallRules)
		safeg.POST("/add", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.AddFirewallRule)
		safeg.POST("/update", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.UpdateFirewallRule)
		safeg.POST("/del", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.DeleteFirewallRule)
		safeg.POST("/rules/state", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.SetFirewallRuleState)
		safeg.POST("/rules/batch", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.BatchFirewallRules)
		safeg.POST("/rules/cleanup", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.CleanupFirewallRules)
		safeg.GET("/rules/export", middleware.RequirePermission(accessservice.PermissionSecurityRead), safe.ExportFirewallRules)
		safeg.POST("/rules/import", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.ImportFirewallRules)
		safeg.POST("/forwards", middleware.RequirePermission(accessservice.PermissionSecurityRead), safe.ListPortForwards)
		safeg.POST("/forwards/add", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.AddPortForward)
		safeg.POST("/forwards/update", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.UpdatePortForward)
		safeg.POST("/forwards/del", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.DeletePortForward)
		safeg.POST("/forwards/state", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.SetPortForwardState)
		safeg.GET("/auto-block", middleware.RequirePermission(accessservice.PermissionSecurityRead), safe.GetAutoBlockConfig)
		safeg.POST("/auto-block", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.SaveAutoBlockConfig)
		safeg.POST("/auto-block/run", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.RunAutoBlock)
		safeg.POST("/stop", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.StopFirewall)
		safeg.POST("/blockping", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.BlockPing)
		safeg.POST("/install", middleware.RequirePermission(accessservice.PermissionSecurityWrite), safe.InstallFirewall)
	}

	// SSH 相关
	sshg := protected.Group("/ssh")
	{
		sshg.GET("/status", middleware.RequirePermission(accessservice.PermissionTerminalAccess), ssh.Status)
		sshg.GET("/sessions", middleware.RequirePermission(accessservice.PermissionTerminalAccess), ssh.Sessions)
		sshg.POST("/ticket", middleware.RequirePermission(accessservice.PermissionTerminalAccess), ssh.CreateTicket)
		sshg.GET("/open", middleware.RequirePermission(accessservice.PermissionTerminalAccess), ssh.OpenSSH)
	}

	// 定时任务相关
	crong := protected.Group("/cron")
	{
		crong.GET("/templates", middleware.RequirePermission(accessservice.PermissionCronRead), cron.ListTemplates)
		crong.GET("/executions/running", middleware.RequirePermission(accessservice.PermissionCronRead), cron.ListRunningExecutions)
		crong.POST("/executions/:id/cancel", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.CancelExecution)
		crong.POST("/list", middleware.RequirePermission(accessservice.PermissionCronRead), cron.GetCronList)
		crong.POST("/add", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.AddCron)
		crong.POST("/update", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.UpdateCron)
		crong.POST("/del", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.DeleteCron)
		crong.POST("/disable", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.DisableCron)
		crong.POST("/enable", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.EnableCron)
		crong.POST("/log", middleware.RequirePermission(accessservice.PermissionCronRead), cron.GetCronLogList)
		crong.POST("/log/cleanup", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.CleanupCronLogs)
		crong.GET("/:id/log/export", middleware.RequirePermission(accessservice.PermissionCronRead), cron.ExportCronLogs)
		crong.POST("/run", middleware.RequirePermission(accessservice.PermissionCronWrite), cron.RunCron)
	}

	monitoringg := protected.Group("/monitor")
	{
		monitoringg.GET("/summary", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.Summary)
		monitoringg.GET("/services", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.ServiceHealth)
		monitoringg.POST("/services/check", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.CheckServiceHealth)
		monitoringg.POST("/services/:component/silence", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.SilenceServiceHealth)
		monitoringg.GET("/metrics", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.Metrics)
		monitoringg.GET("/history", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.History)
		monitoringg.GET("/rules", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.ListRules)
		monitoringg.POST("/rules", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.CreateRule)
		monitoringg.PUT("/rules/:id", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.UpdateRule)
		monitoringg.DELETE("/rules/:id", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.DeleteRule)
		monitoringg.POST("/rules/:id/update", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.UpdateRule)
		monitoringg.POST("/rules/:id/delete", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.DeleteRule)
		monitoringg.POST("/rules/:id/silence", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.SilenceRule)
		monitoringg.GET("/events", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.Events)
		monitoringg.GET("/deliveries", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.Deliveries)
		monitoringg.GET("/channels", middleware.RequirePermission(accessservice.PermissionMonitoringRead), monitoringHandler.ListChannels)
		monitoringg.POST("/channels", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.CreateChannel)
		monitoringg.PUT("/channels/:id", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.UpdateChannel)
		monitoringg.DELETE("/channels/:id", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.DeleteChannel)
		monitoringg.POST("/channels/:id/update", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.UpdateChannel)
		monitoringg.POST("/channels/:id/delete", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.DeleteChannel)
		monitoringg.POST("/channels/:id/test", middleware.RequirePermission(accessservice.PermissionMonitoringWrite), monitoringHandler.TestChannel)
	}

	// Docker 容器管理。所有资源操作均由固定动作适配器执行，不接受任意命令。
	containerg := protected.Group("/containers")
	{
		containerg.GET("/runtime", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.Runtime)
		containerg.POST("/runtime/actions", middleware.RequirePermission(accessservice.PermissionContainerConfigWrite), containerHandler.RuntimeAction)
		containerg.GET("", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ListContainers)
		containerg.POST("", middleware.RequirePermission(accessservice.PermissionContainerWrite), containerHandler.CreateContainer)
		containerg.POST("/cleanup", middleware.RequirePermission(accessservice.PermissionContainerDangerousCleanup), containerHandler.Cleanup)
		containerg.GET("/tasks", middleware.RequirePermission(accessservice.PermissionContainerRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll, accessservice.PermissionContainerComposeWrite), containerHandler.ListContainerTasks)
		containerg.GET("/tasks/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll, accessservice.PermissionContainerComposeWrite), containerHandler.GetContainerTask)
		containerg.GET("/tasks/:id/events", middleware.RequirePermission(accessservice.PermissionContainerRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll, accessservice.PermissionContainerComposeWrite), containerHandler.StreamContainerTaskEvents)
		containerg.GET("/tasks/:id/log", middleware.RequirePermission(accessservice.PermissionContainerRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll, accessservice.PermissionContainerComposeWrite), containerHandler.GetContainerTaskLog)
		containerg.GET("/tasks/:id/log/download", middleware.RequirePermission(accessservice.PermissionContainerRead), middleware.RequireAnyPermission(accessservice.PermissionTaskReadSelf, accessservice.PermissionTaskReadAll, accessservice.PermissionContainerComposeWrite), containerHandler.DownloadContainerTaskLog)
		containerg.POST("/tasks/:id/cancel", middleware.RequireAnyPermission(accessservice.PermissionContainerWrite, accessservice.PermissionContainerImageWrite, accessservice.PermissionContainerComposeWrite), middleware.RequirePermission(accessservice.PermissionTaskCancelSelf), containerHandler.CancelContainerTask)
		containerg.GET("/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.GetContainer)
		containerg.GET("/:id/stats", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ContainerStats)
		containerg.POST("/:id/actions", middleware.RequirePermission(accessservice.PermissionContainerWrite), containerHandler.Action)
		containerg.POST("/:id/networks", middleware.RequirePermission(accessservice.PermissionContainerNetworkWrite), containerHandler.ContainerNetworkAction)
		containerg.POST("/batch/actions", middleware.RequirePermission(accessservice.PermissionContainerWrite), containerHandler.BatchAction)
		containerg.GET("/:id/terminal/status", middleware.RequirePermission(accessservice.PermissionContainerTerminal), containerHandler.TerminalStatus)
		containerg.POST("/:id/terminal/ticket", middleware.RequirePermission(accessservice.PermissionContainerTerminal), containerHandler.CreateTerminalTicket)
		containerg.GET("/:id/terminal/open", middleware.RequirePermission(accessservice.PermissionContainerTerminal), containerHandler.OpenTerminal)
		containerg.GET("/:id/logs", middleware.RequirePermission(accessservice.PermissionContainerLogsRead), containerHandler.Logs)
		containerg.GET("/:id/logs/download", middleware.RequirePermission(accessservice.PermissionContainerLogsRead), containerHandler.DownloadLogs)
		containerg.GET("/images", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ListImages)
		containerg.POST("/images/import", middleware.RequirePermission(accessservice.PermissionContainerImageWrite), containerHandler.ImportImage)
		containerg.POST("/images/build", middleware.RequirePermission(accessservice.PermissionContainerImageWrite), containerHandler.BuildImage)
		containerg.POST("/images/build-cache/prune", middleware.RequirePermission(accessservice.PermissionContainerDangerousCleanup), containerHandler.PruneBuildCache)
		containerg.POST("/images/:id/tag", middleware.RequirePermission(accessservice.PermissionContainerImageWrite), containerHandler.TagImage)
		containerg.POST("/images/push", middleware.RequirePermission(accessservice.PermissionContainerImageWrite), containerHandler.PushImage)
		containerg.GET("/images/:id/export", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ExportImage)
		containerg.GET("/images/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.InspectImage)
		containerg.POST("/images/pull", middleware.RequirePermission(accessservice.PermissionContainerImageWrite), containerHandler.PullImage)
		containerg.POST("/images/prune", middleware.RequirePermission(accessservice.PermissionContainerDangerousCleanup), containerHandler.PruneImages)
		containerg.DELETE("/images/:id", middleware.RequirePermission(accessservice.PermissionContainerDelete), containerHandler.DeleteImage)
		containerg.GET("/networks", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ListNetworks)
		containerg.GET("/networks/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.InspectNetwork)
		containerg.POST("/networks", middleware.RequirePermission(accessservice.PermissionContainerNetworkWrite), containerHandler.CreateNetwork)
		containerg.POST("/networks/prune", middleware.RequirePermission(accessservice.PermissionContainerDangerousCleanup), containerHandler.PruneNetworks)
		containerg.DELETE("/networks/:id", middleware.RequirePermission(accessservice.PermissionContainerNetworkWrite), containerHandler.DeleteNetwork)
		containerg.POST("/networks/batch/delete", middleware.RequirePermission(accessservice.PermissionContainerNetworkWrite), containerHandler.BatchDeleteNetwork)
		containerg.GET("/volumes", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ListVolumes)
		containerg.GET("/volumes/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.InspectVolume)
		containerg.POST("/volumes", middleware.RequirePermission(accessservice.PermissionContainerVolumeWrite), containerHandler.CreateVolume)
		containerg.POST("/volumes/prune", middleware.RequirePermission(accessservice.PermissionContainerDangerousCleanup), containerHandler.PruneVolumes)
		containerg.DELETE("/volumes/:id", middleware.RequirePermission(accessservice.PermissionContainerVolumeWrite), containerHandler.DeleteVolume)
		containerg.POST("/volumes/batch/delete", middleware.RequirePermission(accessservice.PermissionContainerVolumeWrite), containerHandler.BatchDeleteVolume)
		containerg.GET("/registries", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.Registries)
		containerg.POST("/registries", middleware.RequirePermission(accessservice.PermissionContainerRegistryWrite), containerHandler.CreateRegistry)
		containerg.PUT("/registries/:id", middleware.RequirePermission(accessservice.PermissionContainerRegistryWrite), containerHandler.UpdateRegistry)
		containerg.DELETE("/registries/:id", middleware.RequirePermission(accessservice.PermissionContainerRegistryWrite), containerHandler.DeleteRegistry)
		containerg.POST("/registries/:id/test", middleware.RequirePermission(accessservice.PermissionContainerRegistryWrite), containerHandler.TestRegistry)
		containerg.GET("/compose", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.ListCompose)
		containerg.POST("/compose/preview", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.PreviewCompose)
		containerg.POST("/compose", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.CreateCompose)
		containerg.GET("/compose/:name", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.GetCompose)
		containerg.GET("/compose/:name/config", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.GetComposeConfig)
		containerg.POST("/compose/:name/config/reveal", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.RevealComposeConfig)
		containerg.PUT("/compose/:name/config", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.UpdateComposeConfig)
		containerg.POST("/compose/:name/actions", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.ComposeAction)
		containerg.GET("/compose/:name/logs", middleware.RequirePermission(accessservice.PermissionContainerLogsRead), containerHandler.ComposeLogs)
		containerg.GET("/templates", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.Templates)
		containerg.POST("/templates", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.CreateTemplate)
		containerg.GET("/templates/:id", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.GetTemplate)
		containerg.PUT("/templates/:id", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.UpdateTemplate)
		containerg.DELETE("/templates/:id", middleware.RequirePermission(accessservice.PermissionContainerComposeWrite), containerHandler.DeleteTemplate)
		containerg.GET("/config", middleware.RequirePermission(accessservice.PermissionContainerRead), containerHandler.Config)
		containerg.PUT("/config", middleware.RequirePermission(accessservice.PermissionContainerConfigWrite), containerHandler.SaveConfig)
		containerg.POST("/config", middleware.RequirePermission(accessservice.PermissionContainerConfigWrite), containerHandler.SaveConfig)
	}

	// 堡垒机管理
	bastiong := protected.Group("/bastion")
	{
		bastiong.GET("/overview", middleware.RequirePermission(accessservice.PermissionBastionRead), bastionHandler.Overview)
		bastiong.GET("/servers", middleware.RequirePermission(accessservice.PermissionBastionRead), bastionHandler.ListServers)
		bastiong.GET("/servers/:id", middleware.RequirePermission(accessservice.PermissionBastionRead), bastionHandler.GetServer)
		bastiong.POST("/servers", middleware.RequirePermission(accessservice.PermissionBastionWrite), bastionHandler.CreateServer)
		bastiong.PUT("/servers/:id", middleware.RequirePermission(accessservice.PermissionBastionWrite), bastionHandler.UpdateServer)
		bastiong.DELETE("/servers/:id", middleware.RequirePermission(accessservice.PermissionBastionWrite), bastionHandler.DeleteServer)
		bastiong.POST("/servers/:id/test", middleware.RequirePermission(accessservice.PermissionBastionRead), bastionHandler.TestConnection)
		bastiong.GET("/servers/:id/metrics", middleware.RequirePermission(accessservice.PermissionBastionRead), bastionHandler.GetMetrics)
	}

	return r
}
