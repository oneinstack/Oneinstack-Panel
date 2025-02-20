package server

import (
	"log"
	"oneinstack/app"
	"oneinstack/internal/services/software"
)

// 用作启动后端持久化服务&初始化服务
func Start() {
	app.Viper()

	if err := app.InitDB(app.GetBasePath() + "myadmin.db"); err != nil {
		log.Fatal("InitDB error:", err)
	}
	go software.Sync()
}
