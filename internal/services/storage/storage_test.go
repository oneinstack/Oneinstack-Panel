package storage

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
	"oneinstack/utils"

	"gorm.io/gorm"
)

type fakeStorageOperation struct {
	connected bool
	closed    bool
	created   *models.Library
	deleted   *models.Library
	password  string
}

func (f *fakeStorageOperation) Connect() error {
	f.connected = true
	return nil
}

func (f *fakeStorageOperation) Close() error {
	f.closed = true
	return nil
}

func (f *fakeStorageOperation) Sync() error { return nil }

func (f *fakeStorageOperation) CreateLibrary(library *models.Library) error {
	copy := *library
	f.created = &copy
	return nil
}

func (f *fakeStorageOperation) DeleteLibrary(library *models.Library) error {
	copy := *library
	f.deleted = &copy
	return nil
}

func (f *fakeStorageOperation) UpdateLibraryPassword(library *models.Library, password string) error {
	copy := *library
	f.created = &copy
	f.password = password
	return nil
}

func prepareStorageTest(t *testing.T) {
	t.Helper()
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x61}, 32)); err != nil {
		t.Fatal(err)
	}
	if err := app.InitDB(filepath.Join(t.TempDir(), "storage.db")); err != nil {
		t.Fatal(err)
	}
	previousFactory := newStorageOP
	t.Cleanup(func() {
		newStorageOP = previousFactory
	})
}

func TestConnectionCredentialsAreEncryptedAndNeverListed(t *testing.T) {
	prepareStorageTest(t)
	var connectedPassword string
	operation := &fakeStorageOperation{}
	newStorageOP = func(storage *models.Storage, _ string) (StorageOPI, error) {
		connectedPassword = storage.Password
		return operation, nil
	}
	if err := Add(&input.AddParam{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: "root-database-secret", Type: "mysql", Remark: "local",
	}); err != nil {
		t.Fatal(err)
	}
	if connectedPassword != "root-database-secret" || !operation.connected || !operation.closed {
		t.Fatalf("connection was not tested with the plaintext credential")
	}

	var stored models.Storage
	if err := app.DB().Where("addr = ?", "127.0.0.1").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !utils.IsEncryptedCredential(stored.Password) ||
		strings.Contains(stored.Password, "root-database-secret") {
		t.Fatalf("stored password was not encrypted: %q", stored.Password)
	}
	list, err := List("mysql")
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "root-database-secret") ||
		strings.Contains(string(serialized), `"password"`) ||
		!strings.Contains(string(serialized), `"passwordConfigured":true`) {
		t.Fatalf("connection response exposed or omitted credential state: %s", serialized)
	}
}

func TestManagedLocalMySQLCredentialDecryptsExistingCredential(t *testing.T) {
	prepareStorageTest(t)
	encrypted, err := utils.EncryptCredential(
		"existing-root-secret",
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Create(&models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: encrypted, Type: "mysql", Remark: "本机 MySQL（面板自动管理）",
	}).Error; err != nil {
		t.Fatal(err)
	}
	username, password, found, err := ManagedLocalMySQLCredential("3306")
	if err != nil {
		t.Fatal(err)
	}
	if !found || username != "root" || password != "existing-root-secret" {
		t.Fatalf("unexpected managed MySQL credential: %q %q %v", username, password, found)
	}
}

func TestUpdateWithBlankPasswordRetainsExistingCredential(t *testing.T) {
	prepareStorageTest(t)
	newStorageOP = func(_ *models.Storage, _ string) (StorageOPI, error) {
		return &fakeStorageOperation{}, nil
	}
	if err := Add(&input.AddParam{
		Addr: "127.0.0.1", Port: "6379", Root: "default",
		Password: "redis-secret", Type: "redis",
	}); err != nil {
		t.Fatal(err)
	}
	var stored models.Storage
	if err := app.DB().Where("type = ?", "redis").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var testedPassword string
	newStorageOP = func(candidate *models.Storage, _ string) (StorageOPI, error) {
		testedPassword = candidate.Password
		return &fakeStorageOperation{}, nil
	}
	if err := Update(&input.AddParam{
		ID: stored.ID, Addr: "localhost", Port: "6379",
		Root: "default", Password: "", Type: "redis", Remark: "updated",
	}); err != nil {
		t.Fatal(err)
	}
	if testedPassword != "redis-secret" {
		t.Fatalf("blank update did not reuse stored credential: %q", testedPassword)
	}
	reloaded, err := loadStorage(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Password != "redis-secret" || reloaded.Remark != "updated" {
		t.Fatalf("unexpected updated connection: %#v", reloaded)
	}
}

func TestListRestoresManagedLocalRedisConnection(t *testing.T) {
	prepareStorageTest(t)
	operation := &fakeStorageOperation{}
	var connected *models.Storage
	newStorageOP = func(candidate *models.Storage, _ string) (StorageOPI, error) {
		copy := *candidate
		connected = &copy
		return operation, nil
	}
	if err := app.DB().Create(&models.Software{
		Name: "Redis", Key: "redis", Component: "redis", Version: "8.4.0",
		Installed: true, Status: models.Soft_Status_Suc,
	}).Error; err != nil {
		t.Fatal(err)
	}
	connections, err := List("redis")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 {
		t.Fatalf("expected one restored Redis connection, got %#v", connections)
	}
	if connected == nil || connected.Addr != "127.0.0.1" ||
		connected.Port != "6379" || connected.Root != "default" || connected.Password != "" {
		t.Fatalf("unexpected Redis connection probe: %#v", connected)
	}
	if !operation.connected || !operation.closed {
		t.Fatal("restored Redis connection was not verified")
	}
	var stored models.Storage
	if err := app.DB().Where("type = ?", "redis").First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Remark != "本机 Redis（面板自动管理）" || stored.Password != "" {
		t.Fatalf("unexpected stored Redis connection: %#v", stored)
	}
}

func TestMySQLLibraryLifecycleUsesRemoteOperationAndEncryptedMetadata(t *testing.T) {
	prepareStorageTest(t)
	connectionPassword, err := utils.EncryptCredential(
		"root-secret",
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		t.Fatal(err)
	}
	connection := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: connectionPassword, Type: "mysql",
	}
	if err := app.DB().Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	operation := &fakeStorageOperation{}
	var operationPassword string
	newStorageOP = func(storage *models.Storage, library string) (StorageOPI, error) {
		operationPassword = storage.Password
		if library != "site_db" && library != "" {
			t.Fatalf("unexpected library target %q", library)
		}
		return operation, nil
	}
	request := &input.LibParam{
		ID: connection.ID, Name: "site_db", Root: "site_user",
		Password: "site-user-secret", Encoding: "utf8mb4",
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := AddLibs(request); err != nil {
		t.Fatal(err)
	}
	if operationPassword != "root-secret" ||
		operation.created == nil ||
		operation.created.Password != "site-user-secret" {
		t.Fatalf("remote create operation did not receive decrypted credentials: %#v", operation.created)
	}
	var library models.Library
	if err := app.DB().Where("name = ?", "site_db").First(&library).Error; err != nil {
		t.Fatal(err)
	}
	if !utils.IsEncryptedCredential(library.Password) {
		t.Fatalf("library credential remained plaintext: %q", library.Password)
	}
	page, err := LibList(&input.QueryParam{
		Type: "mysql",
		Page: input.Page{Page: 1, PageSize: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(page)
	if strings.Contains(string(serialized), "site-user-secret") ||
		strings.Contains(string(serialized), `"password"`) {
		t.Fatalf("library list exposed a credential: %s", serialized)
	}
	if err := DeleteLibrary(&input.DeleteLibraryParam{
		ID: library.ID, ConfirmName: "wrong_name",
	}); err == nil {
		t.Fatal("database deletion accepted an incorrect confirmation name")
	}
	if err := DeleteLibrary(&input.DeleteLibraryParam{
		ID: library.ID, ConfirmName: "site_db",
	}); err != nil {
		t.Fatal(err)
	}
	if operation.deleted == nil || operation.deleted.Name != "site_db" {
		t.Fatalf("remote database delete was not executed: %#v", operation.deleted)
	}
}

func TestManagedMySQLLibraryGeneratesCredentialAndCanRotateIt(t *testing.T) {
	prepareStorageTest(t)
	connectionPassword, err := utils.EncryptCredential(
		"root-secret",
		utils.CredentialPurposeStoragePassword,
	)
	if err != nil {
		t.Fatal(err)
	}
	connection := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root",
		Password: connectionPassword, Type: "mysql",
	}
	if err := app.DB().Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	operation := &fakeStorageOperation{}
	newStorageOP = func(_ *models.Storage, _ string) (StorageOPI, error) {
		return operation, nil
	}
	request := &input.LibParam{
		ID: connection.ID, Name: "managed_db", Encoding: "utf8mb4",
	}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	credential, err := AddLibs(request)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Username != "managed_db" ||
		credential.Database != "managed_db" ||
		len(credential.Password) != 24 {
		t.Fatalf("unexpected generated database credential: %#v", credential)
	}
	if operation.created == nil ||
		operation.created.User != "managed_db" ||
		operation.created.Password != credential.Password {
		t.Fatalf("database was not created with generated credential: %#v", operation.created)
	}
	revealed, err := GetLibraryCredential(credential.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if *revealed != *credential {
		t.Fatalf("revealed credential = %#v, want %#v", revealed, credential)
	}
	rotated, err := UpdateLibraryCredential(credential.LibraryID, "")
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Password == credential.Password || len(rotated.Password) != 24 {
		t.Fatalf("password rotation did not generate a new credential: %#v", rotated)
	}
	if operation.password != rotated.Password {
		t.Fatalf("remote account received %q, want %q", operation.password, rotated.Password)
	}
	revealed, err = GetLibraryCredential(credential.LibraryID)
	if err != nil {
		t.Fatal(err)
	}
	if revealed.Password != rotated.Password {
		t.Fatalf("stored rotated credential = %q, want %q", revealed.Password, rotated.Password)
	}
}

func TestMySQLSyncPreservesLibraryIdentityCredentialsAndBackupLinks(t *testing.T) {
	prepareStorageTest(t)
	connection := &models.Storage{
		Addr: "127.0.0.1", Port: "3306", Root: "root", Type: "mysql",
	}
	if err := app.DB().Create(connection).Error; err != nil {
		t.Fatal(err)
	}
	existing := &models.Library{
		PID: connection.ID, Name: "site_db", User: "old_user",
		Password: "enc:v1:preserve-me", Encoding: "utf8mb4", Type: "mysql",
	}
	missing := &models.Library{
		PID: connection.ID, Name: "removed_db", Type: "mysql",
	}
	if err := app.DB().Create(existing).Error; err != nil {
		t.Fatal(err)
	}
	if err := app.DB().Create(missing).Error; err != nil {
		t.Fatal(err)
	}
	backup := &models.DatabaseBackup{
		ID: "backup-link", LibraryID: existing.ID, ConnectionID: connection.ID,
		DatabaseName: existing.Name, Source: models.DatabaseBackupSourceManual,
		FileName: "site_db.sql.gz", FilePath: "/safe/site_db.sql.gz",
		SizeBytes: 1, SHA256: strings.Repeat("a", 64), CreatedBy: 1,
	}
	if err := app.DB().Create(backup).Error; err != nil {
		t.Fatal(err)
	}

	err := app.DB().Transaction(func(tx *gorm.DB) error {
		return persistSyncedMySQLLibraries(tx, connection.ID, []models.Library{
			{
				PID: connection.ID, Name: "site_db", User: "remote_user",
				Encoding: "utf8mb4", Capacity: "2.00 MB",
				PAddr: "127.0.0.1:3306", Type: "mysql",
			},
			{
				PID: connection.ID, Name: "new_db", Encoding: "utf8mb4",
				Capacity: "0 B", PAddr: "127.0.0.1:3306", Type: "mysql",
			},
		})
	})
	if err != nil {
		t.Fatal(err)
	}

	var synced models.Library
	if err := app.DB().First(&synced, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if synced.Name != "site_db" ||
		synced.Password != "enc:v1:preserve-me" ||
		synced.User != "remote_user" ||
		synced.Capacity != "2.00 MB" {
		t.Fatalf("existing library was not updated safely: %+v", synced)
	}
	var linked models.DatabaseBackup
	if err := app.DB().First(&linked, "id = ?", backup.ID).Error; err != nil {
		t.Fatal(err)
	}
	if linked.LibraryID != existing.ID {
		t.Fatalf("backup link changed from %d to %d", existing.ID, linked.LibraryID)
	}
	var removed int64
	if err := app.DB().Model(&models.Library{}).Where("id = ?", missing.ID).
		Count(&removed).Error; err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatal("library missing from remote sync was not removed")
	}
	var added models.Library
	if err := app.DB().Where("p_id = ? AND name = ?", connection.ID, "new_db").
		First(&added).Error; err != nil {
		t.Fatal(err)
	}
}
