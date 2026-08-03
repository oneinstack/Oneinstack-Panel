package server

import (
	"fmt"
	"log"
	"oneinstack/app"
	bastionservice "oneinstack/internal/services/bastion"
	"oneinstack/internal/services/software"
	"oneinstack/internal/services/user"
)

// 用作启动后端持久化服务&初始化服务
func Start() {
	app.Viper()

	// 检查是否有用户，没有则自动创建admin用户
	initializeDefaultUser()

	go software.StartCatalogSync()

	// 初始化堡垒机模块（可选安装项）
	initializeBastion()
}

// initializeBastion 根据配置初始化堡垒机管理器
func initializeBastion() {
	cfg := app.ONE_CONFIG.Bastion
	if !cfg.Enabled {
		return
	}
	db := app.DB()
	if db == nil {
		log.Printf("堡垒机: 数据库未初始化，跳过")
		return
	}
	collector := bastionservice.NewSSHCollector(cfg.CollectTimeoutSeconds)
	manager, err := bastionservice.NewManager(
		db,
		collector,
		cfg.CollectTimeoutSeconds,
		cfg.MaxConcurrentCollects,
		cfg.RetentionDays,
		cfg.CollectSchedule,
		cfg.CleanupSchedule,
	)
	if err != nil {
		log.Printf("堡垒机: 初始化失败: %v", err)
		return
	}
	bastionservice.ConfigureDefault(manager)
	manager.Start()
	log.Printf("堡垒机: 模块已启用 (采集: %s, 保留: %d 天)", cfg.CollectSchedule, cfg.RetentionDays)
}

// initializeDefaultUser 初始化默认用户
func initializeDefaultUser() {
	hasUser, err := user.HasUser()
	if err != nil {
		log.Printf("检查用户失败: %v", err)
		return
	}

	if !hasUser {
		username, password, err := user.CreateAdminUser()
		if err != nil {
			log.Printf("创建默认admin用户失败: %v", err)
			return
		}

		fmt.Printf("\n🎉 首次启动检测到无用户，已自动创建管理员账户：\n")
		fmt.Printf("📝 用户名: %s\n", username)
		fmt.Printf("🔐 密码: %s\n", password)
		fmt.Printf("⚠️ 请妥善保存上述信息！\n\n")
	}
}
