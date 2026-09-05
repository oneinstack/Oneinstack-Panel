package app

import (
	"oneinstack/config"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/viper"
	"gorm.io/gorm"
)

var (
	db         *gorm.DB
	dbMu       sync.RWMutex
	dbInitMu   sync.Mutex
	ONE_CONFIG config.Server
	ONE_VIP    *viper.Viper
)

var BASE_PATH = resolveBasePath()
var ENV = ""

func DB() *gorm.DB {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return db
}

func setDB(database *gorm.DB) {
	dbMu.Lock()
	db = database
	dbMu.Unlock()
}

func GetBasePath() string {
	return BASE_PATH
}

func IsDevelopmentEnvironment() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("GO_ENV")), "development") ||
		strings.EqualFold(strings.TrimSpace(ENV), "debug")
}

func resolveBasePath() string {
	if path := strings.TrimSpace(os.Getenv("ONEINSTACK_BASE_PATH")); path != "" {
		return normalizeBasePath(path)
	}
	if os.Getenv("GO_ENV") == "development" {
		return DefaultDevelopmentBasePath()
	}
	return normalizeBasePath("/usr/local/one")
}

func DefaultDevelopmentBasePath() string {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return normalizeBasePath(filepath.Join(os.TempDir(), "oneinstack-panel", "runtime"))
	}
	return normalizeBasePath(filepath.Join(workingDirectory, ".runtime"))
}

func normalizeBasePath(path string) string {
	return filepath.Clean(path) + string(os.PathSeparator)
}
