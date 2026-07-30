package software

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestComponentHealthCollectorSkipsUninstalledAndBusyServices(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "health.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.Software{}, &models.SoftwareTask{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.Software{
		Name: "Redis", Key: "redis", Component: "redis", Version: "7.4.8",
		Installed: true, InstallVersion: "7.4.8", Status: models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.SoftwareTask{
		ID: "12345678-1234-1234-1234-123456789012", Operation: "restart",
		Component: "redis", SoftwareKey: "redis", RequestedVersion: "7.4.8",
		Status: models.SoftwareTaskStatusRestarting, Phase: "restart",
		RollbackStatus: models.SoftwareTaskRollbackNotRequired, RequestedBy: 1,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}
	collector := NewComponentHealthCollector(database)
	observations, err := collector.CollectServiceHealth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != len(SupportedComponentServices()) {
		t.Fatalf("health observations = %d", len(observations))
	}
	var redisFound bool
	for _, observation := range observations {
		if observation.Component == "redis" {
			redisFound = true
			if !observation.Installed || !observation.Busy ||
				observation.ServiceState != "transitioning" {
				t.Fatalf("unexpected busy redis observation: %#v", observation)
			}
			continue
		}
		if observation.Installed || observation.ServiceState != "not_installed" {
			t.Fatalf("unexpected uninstalled observation: %#v", observation)
		}
	}
	if !redisFound {
		t.Fatal("redis health observation is missing")
	}
}
