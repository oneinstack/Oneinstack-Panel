package software

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestConfigurationHistoryListAndLookup(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "history.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.SoftwareConfigurationHistory{}); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"workerProcesses":   "auto",
		"workerConnections": "4096",
		"keepaliveTimeout":  "65",
		"clientMaxBodySize": "1",
	}
	beforeJSON, _ := json.Marshal(values)
	afterValues := map[string]string{}
	for key, value := range values {
		afterValues[key] = value
	}
	afterValues["workerConnections"] = "8192"
	afterJSON, _ := json.Marshal(afterValues)
	now := time.Now()
	rows := []models.SoftwareConfigurationHistory{
		{
			ID:              uuid.NewString(),
			TaskID:          uuid.NewString(),
			Component:       "nginx",
			SoftwareKey:     "webserver",
			SoftwareVersion: "1.28.0",
			BaseRevision:    "oldest",
			BeforeJSON:      string(beforeJSON),
			AfterJSON:       string(afterJSON),
			Status:          models.SoftwareConfigurationStatusSucceeded,
			RequestedBy:     1,
			CreatedAt:       now.Add(-time.Minute),
		},
		{
			ID:              uuid.NewString(),
			TaskID:          uuid.NewString(),
			Component:       "nginx",
			SoftwareKey:     "webserver",
			SoftwareVersion: "1.28.0",
			BaseRevision:    "newest",
			BeforeJSON:      string(afterJSON),
			AfterJSON:       string(beforeJSON),
			Status:          models.SoftwareConfigurationStatusFailed,
			RequestedBy:     1,
			CreatedAt:       now,
		},
	}
	if err := database.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	page, err := ListConfigurationHistory(database, "webserver", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 1 ||
		page.Items[0].BaseRevision != "newest" ||
		page.Items[0].Before["workerConnections"] != "8192" {
		t.Fatalf("unexpected history page: %#v", page)
	}

	entry, err := GetConfigurationHistory(database, "nginx", rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != models.SoftwareConfigurationStatusSucceeded ||
		entry.After["workerConnections"] != "8192" {
		t.Fatalf("unexpected history entry: %#v", entry)
	}
}
