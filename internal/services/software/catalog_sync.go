package software

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"oneinstack/app"
	catalogService "oneinstack/internal/services/softwarecatalog"

	"gorm.io/gorm"
)

var (
	catalogManagerMu sync.Mutex
	catalogManager   *catalogService.Manager
	catalogManagerDB *gorm.DB
)

func defaultCatalogManager() (*catalogService.Manager, error) {
	catalogManagerMu.Lock()
	defer catalogManagerMu.Unlock()
	db := app.DB()
	if db == nil {
		return nil, fmt.Errorf("database is not initialized")
	}
	if catalogManager == nil || catalogManagerDB != db {
		manager, err := catalogService.New(app.ONE_CONFIG.ScriptCenter, db)
		if err != nil {
			return nil, err
		}
		catalogManager = manager
		catalogManagerDB = db
	}
	return catalogManager, nil
}

func SyncCatalogNow(ctx context.Context) (catalogService.Status, error) {
	manager, err := defaultCatalogManager()
	if err != nil {
		return catalogService.Status{}, err
	}
	return manager.Sync(ctx)
}

func GetCatalogStatus() (catalogService.Status, error) {
	manager, err := defaultCatalogManager()
	if err != nil {
		return catalogService.Status{}, err
	}
	return manager.Status()
}

// StartCatalogSync immediately refreshes the signed Center catalog and then
// maintains the local offline snapshot at the configured interval.
func StartCatalogSync() {
	manager, err := defaultCatalogManager()
	if err != nil {
		log.Printf("初始化 Center 软件目录失败: %v", err)
		return
	}
	if !app.ONE_CONFIG.ScriptCenter.Enabled {
		return
	}
	syncOnce := func() {
		timeout := time.Duration(app.ONE_CONFIG.ScriptCenter.RequestTimeoutSeconds+5) * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		status, syncErr := manager.Sync(ctx)
		if syncErr != nil {
			log.Printf(
				"同步 Center 软件目录失败，继续使用本地快照 mode=%s revision=%s: %v",
				status.Mode,
				status.Revision,
				syncErr,
			)
		}
	}
	syncOnce()
	ticker := time.NewTicker(
		time.Duration(app.ONE_CONFIG.ScriptCenter.CatalogSyncIntervalMinutes) * time.Minute,
	)
	defer ticker.Stop()
	for range ticker.C {
		syncOnce()
	}
}
