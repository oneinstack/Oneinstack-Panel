package utils

import (
	"log"
	"os"
)

func EnsureOneDir() {
	dirPath := "/usr/local/one"

	// 检查目录是否存在
	if _, err := os.Stat(dirPath); os.IsNotExist(err) {
		log.Printf("目录 %s 不存在，正在创建...", dirPath)

		// 递归创建目录
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			log.Fatalf("创建目录失败: %v", err)
		}

		// 设置权限（可选）
		if err := os.Chmod(dirPath, 0755); err != nil {
			log.Printf("警告：无法设置目录权限: %v", err)
		}
		log.Println("目录创建成功")
	}
}
