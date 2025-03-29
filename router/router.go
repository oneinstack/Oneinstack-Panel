package web

import (
	"oneinstack/router/handler/cron"
	"oneinstack/router/handler/ftp"
	"oneinstack/router/handler/safe"
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
	r := gin.Default()

	r.Use(middleware.MidUiHandle)
	g := r.Group("/v1")

	// 公共路由
	{
		g.POST("/login", user.LoginHandler)
	}

	//系统接口
	sys := g.Group("/sys")
	sys.Use(middleware.AuthMiddleware())
	{
		sys.GET("/info", system.GetSystemInfo)
		sys.GET("/monitor", system.GetSystemMonitor)
		sys.GET("/libcount", system.GetLibCount)
		sys.GET("/websitecount", system.GetWebSiteCount)
		sys.GET("/remarkcount", system.GetRemarkCount)
		sys.GET("/systeminfo", system.SystemInfo)
		sys.POST("/updateuser", system.UpdateUser)
		sys.POST("/resetpassword", system.ResetPassword)
		sys.POST("/updateport", system.UpdatePort)
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

	//系统接口
	sysNoAuth := g.Group("/sys")
	{
		sysNoAuth.GET("/getbaseinfo", system.GetInfo)
	}

	//数据库相关
	storageg := g.Group("/storage")
	sys.Use(middleware.AuthMiddleware())
	{
		storageg.POST("/addconn", storage.ADDStorage)
		storageg.POST("/addlib", storage.ADDLib)
		storageg.POST("/updateconn", storage.UpdateStorage)
		storageg.POST("/updatelib", storage.UpdateStorage)
		storageg.GET("/connlist", storage.GetStorage)
		storageg.GET("/delconn", storage.DelStorage)
		storageg.POST("/sync", storage.SyncStorage)
		storageg.POST("/liblist", storage.GetLib)
		storageg.POST("/rklist", storage.GetRedisKeys)
		storageg.POST("/info", storage.Info)
	}

	//FTP相关
	ftpg := g.Group("/ftp")
	sys.Use(middleware.AuthMiddleware())
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
	}

	//软件相关
	softg := g.Group("/soft")
	sys.Use(middleware.AuthMiddleware())
	{
		softg.POST("/list", software.GetSoftware)
		softg.GET("/getlog", software.GetLogContent)
		softg.POST("/install", software.RunInstallation)
		softg.POST("/remove", software.RemoveSoftware)
		softg.POST("/exploration", software.Exploration)
	}

	//网站相关
	websiteg := g.Group("/website")
	sys.Use(middleware.AuthMiddleware())
	{
		websiteg.POST("/list", website.List)
		websiteg.POST("/add", website.Add)
		websiteg.POST("/del", website.Delete)
		websiteg.POST("/update", website.Update)
		websiteg.POST("/info", website.Info)
	}

	//安全相关
	safeg := g.Group("/safe")
	sys.Use(middleware.AuthMiddleware())
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

	//SSH相关
	sshg := g.Group("/ssh")
	sys.Use()
	{
		sshg.GET("/open", ssh.OpenSSH)
	}

	//定时任务相关
	crong := g.Group("/cron")
	sys.Use(middleware.AuthMiddleware())
	{
		crong.POST("/list", cron.GetCronList)
		crong.POST("/add", cron.AddCron)
		crong.POST("/update", cron.UpdateCron)
		crong.POST("/del", cron.DeleteCron)
		crong.POST("/disable", cron.DisableCron)
		crong.POST("/enable", cron.EnableCron)
		crong.POST("/log", cron.GetCronLogList)
	}

	return r
}
