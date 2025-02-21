package server

import (
	"oneinstack/app"
	"oneinstack/internal/services/software"
)

// 用作启动后端持久化服务&初始化服务
func Start() {
	app.Viper()

	go software.Sync()
}
