package app

import (
	"oneinstack/config"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

var (
	db         *gorm.DB
	ONE_CONFIG config.Server
	ONE_VIP    *viper.Viper
)

var BASE_PATH = resolveBasePath()
var ENV = ""

func DB() *gorm.DB {
	return db
}

func GetBasePath() string {
	return BASE_PATH
}

func resolveBasePath() string {
	if path := strings.TrimSpace(os.Getenv("ONEINSTACK_BASE_PATH")); path != "" {
		return normalizeBasePath(path)
	}
	return normalizeBasePath("/usr/local/one")
}

func normalizeBasePath(path string) string {
	return filepath.Clean(path) + string(os.PathSeparator)
}
