package server

import (
	"fmt"
	"log"
	"oneinstack/app"
	"oneinstack/internal/services/software"
	"oneinstack/internal/services/user"
)

// 用作启动后端持久化服务&初始化服务
func Start() {
	app.Viper()

	// 检查是否有用户，没有则自动创建admin用户
	initializeDefaultUser()

	go software.StartCatalogSync()
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
