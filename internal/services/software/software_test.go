package software

import (
	"path/filepath"
	"testing"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
)

func TestListUsesInstalledVersionAcrossGroupedCatalogRows(t *testing.T) {
	if err := app.InitDB(filepath.Join(t.TempDir(), "software.db")); err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ?", "webserver").
		Updates(map[string]any{
			"installed":       false,
			"install_version": "",
			"status":          models.Soft_Status_Default,
		}).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "webserver", "1.28.2").
		Updates(map[string]any{
			"installed":       true,
			"install_version": "1.28.2",
			"status":          models.Soft_Status_Suc,
		}).Error; err != nil {
		t.Fatal(err)
	}

	result, err := List(&input.SoftwareParam{
		Key:  "webserver",
		Page: input.Page{Page: 1, PageSize: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("software rows = %d, want 1", len(result.Data))
	}
	item := result.Data[0]
	if !item.Installed ||
		item.InstallVersion != "1.28.2" ||
		item.Status != models.Soft_Status_Suc {
		t.Fatalf("grouped software state is inconsistent: %#v", item)
	}

	installed := true
	result, err = List(&input.SoftwareParam{
		Key:       "webserver",
		Installed: &installed,
		Page:      input.Page{Page: 1, PageSize: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 1 || !result.Data[0].Installed {
		t.Fatalf("installed filter omitted the installed component: %#v", result.Data)
	}

	notInstalled := false
	result, err = List(&input.SoftwareParam{
		Key:       "webserver",
		Installed: &notInstalled,
		Page:      input.Page{Page: 1, PageSize: 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Data) != 0 {
		t.Fatalf("not-installed filter included an installed component: %#v", result.Data)
	}
}
