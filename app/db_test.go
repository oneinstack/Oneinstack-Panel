package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"oneinstack/internal/models"
	"oneinstack/utils"
)

func TestInitializeUsesApplicationDataDirectory(t *testing.T) {
	originalBasePath := BASE_PATH
	originalDB := db
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		BASE_PATH = originalBasePath
		db = originalDB
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	BASE_PATH = t.TempDir()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	for _, name := range []string{"config.yaml", "myadmin.db"} {
		path := filepath.Join(BASE_PATH, name)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s to be created: %v", path, err)
		}
	}
	if DB() == nil {
		t.Fatal("expected database to be initialized")
	}
}

func TestInitDBMigratesPlaintextDatabaseCredentials(t *testing.T) {
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x24}, 32)); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(t.TempDir(), "credentials.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	storage := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: "legacy-root-password", Type: "mysql",
	}
	if err := DB().Create(storage).Error; err != nil {
		t.Fatal(err)
	}
	library := &models.Library{
		PID: storage.ID, Name: "site_db", User: "site_user",
		Password: "legacy-user-password", Type: "mysql",
	}
	if err := DB().Create(library).Error; err != nil {
		t.Fatal(err)
	}

	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	var migratedStorage models.Storage
	if err := DB().First(&migratedStorage, storage.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !utils.IsEncryptedCredential(migratedStorage.Password) {
		t.Fatalf("storage credential remained plaintext: %q", migratedStorage.Password)
	}
	rootPassword, err := utils.DecryptCredential(
		migratedStorage.Password,
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil || rootPassword != "legacy-root-password" {
		t.Fatalf("storage credential migration failed: value=%q err=%v", rootPassword, err)
	}
	var migratedLibrary models.Library
	if err := DB().First(&migratedLibrary, library.ID).Error; err != nil {
		t.Fatal(err)
	}
	userPassword, err := utils.DecryptCredential(
		migratedLibrary.Password,
		utils.CredentialPurposeLibraryPassword,
	)
	if err != nil || userPassword != "legacy-user-password" {
		t.Fatalf("library credential migration failed: value=%q err=%v", userPassword, err)
	}
}

func TestInitializeAddsCenterBackedSoftwareVersions(t *testing.T) {
	originalBasePath := BASE_PATH
	originalDB := db
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		BASE_PATH = originalBasePath
		db = originalDB
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	BASE_PATH = t.TempDir()
	if err := Initialize(); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ key, version string }{
		{"webserver", "1.28.2"},
		{"db", "8.0"},
		{"php", "8.1"},
		{"php", "8.2"},
		{"php", "8.3"},
		{"redis", "7.4.8"},
	} {
		var count int64
		if err := DB().Model(&models.Software{}).
			Where("`key` = ? AND version = ?", item.key, item.version).
			Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s %s catalog count = %d", item.key, item.version, count)
		}
	}
	if err := Initialize(); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", "php", "8.3").
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("reinitialize duplicated PHP 8.3: count=%d", count)
	}
}

func TestInitUserValidatesCredentialsAndReportsSetupState(t *testing.T) {
	originalBasePath := BASE_PATH
	originalDB := db
	originalConfig := ONE_CONFIG
	originalViper := ONE_VIP
	t.Cleanup(func() {
		BASE_PATH = originalBasePath
		db = originalDB
		ONE_CONFIG = originalConfig
		ONE_VIP = originalViper
	})

	BASE_PATH = t.TempDir()
	if err := Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	hasUsers, err := HasUsers()
	if err != nil {
		t.Fatalf("HasUsers before initialization: %v", err)
	}
	if hasUsers {
		t.Fatal("expected a fresh database to have no users")
	}

	if err := InitUser("invalid.name", "Str0ng!Secret"); err == nil {
		t.Fatal("expected invalid username to be rejected")
	}
	if err := InitUser("operator", "weak"); err == nil {
		t.Fatal("expected weak password to be rejected")
	}
	if err := InitUser("operator", "Str0ng!Secret"); err != nil {
		t.Fatalf("InitUser: %v", err)
	}

	hasUsers, err = HasUsers()
	if err != nil {
		t.Fatalf("HasUsers after initialization: %v", err)
	}
	if !hasUsers {
		t.Fatal("expected initialized database to contain a user")
	}
	var initialized models.User
	if err := DB().First(&initialized).Error; err != nil {
		t.Fatal(err)
	}
	if !initialized.MustChangePassword || initialized.EffectiveSecurityVersion() != 1 {
		t.Fatalf("initial administrator security state = %#v", initialized)
	}

	// Re-running initialization must remain idempotent and must not replace
	// the existing administrator, even when no new password is supplied.
	if err := InitUser("another", ""); err != nil {
		t.Fatalf("idempotent InitUser: %v", err)
	}
}
