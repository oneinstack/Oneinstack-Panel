package app

import (
	"oneinstack/config"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

var (
	db         *gorm.DB
	ONE_CONFIG config.Server
	ONE_VIP    *viper.Viper
)

var BASE_PATH = "/usr/local/one/"
var ENV = "debug"

func DB() *gorm.DB {
	return db
}

func GetBasePath() string {
	if ENV == "debug" {
		return ""
	}
	return BASE_PATH
}
