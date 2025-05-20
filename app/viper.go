package app

import (
	"fmt"
	"oneinstack/utils"
	"os"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

func Viper(path ...string) *viper.Viper {
	utils.EnsureOneDir() // 新增目录检查
	config := GetBasePath() + "config.yaml"

	// 检查 config.yaml 是否存在
	if _, err := os.Stat(config); os.IsNotExist(err) {
		defaultConfig := `
system:
    port: 8089
    remote: 'http://localhost:8189/v1/sys/update'
    defaultPath: '/data/'
    webPath: '/data/wwwroot/'
    logPath: '/data/wwwlogs/'
    dataPath: '/data/db/'
`
		err := os.WriteFile(config, []byte(defaultConfig), 0644)
		if err != nil {
			panic(fmt.Errorf("无法创建默认配置文件: %s", err))
		}
	}

	v := viper.New()
	v.SetConfigFile(config)
	v.SetConfigType("yaml")

	// 读取配置文件
	err := v.ReadInConfig()
	if err != nil {
		panic(fmt.Errorf("无法读取配置文件: %s", err))
	}

	// 监控配置文件的变化
	v.WatchConfig()
	v.OnConfigChange(func(e fsnotify.Event) {
		fmt.Println("配置文件已更改:", e.Name)
		if err := v.Unmarshal(&ONE_CONFIG); err != nil {
			fmt.Println("无法解析配置文件:", err)
		}
	})

	// 初始化配置
	if err := v.Unmarshal(&ONE_CONFIG); err != nil {
		panic(fmt.Errorf("无法解析配置文件: %s", err))
	}

	return v
}
