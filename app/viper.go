package app

import (
	"fmt"
	"os"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

func Viper(path ...string) *viper.Viper {
	config := GetBasePath() + "config.yaml"

	// 检查 config.yaml 是否存在
	if _, err := os.Stat(config); os.IsNotExist(err) {
		fmt.Println("未找到 config.yaml 文件，创建默认配置文件..." + config)
		defaultConfig := `
system:
    port: 8089
    remote: 'http://localhost:8189/v1/sys/update'
`
		err := os.WriteFile(config, []byte(defaultConfig), 0644)
		if err != nil {
			panic(fmt.Errorf("无法创建默认配置文件: %s", err))
		}
		fmt.Println("默认配置文件已创建: config.yaml")
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
