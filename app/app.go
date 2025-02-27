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

var BASE_PATH = "./"
var ENV = ""

func DB() *gorm.DB {
	return db
}

func GetBasePath() string {
	return BASE_PATH
}
