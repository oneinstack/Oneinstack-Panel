package bastion

import (
	"os"
	"testing"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMain(m *testing.M) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	if err := utils.ConfigureCredentialKey(key); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.BastionServer{}, &models.BastionMetricSample{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	manager, err := NewManager(db, NewSSHCollector(5), 15, 5, 30, "*/1 * * * *", "30 4 * * *")
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return manager
}

func TestCreateServerRequiresPasswordForPasswordAuth(t *testing.T) {
	manager := newTestManager(t)

	_, err := manager.CreateServer(CreateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword, Password: "",
	})
	if err == nil {
		t.Fatal("expected error for empty password, got nil")
	}
}

func TestUpdateServerKeepsExistingPasswordWhenOmitted(t *testing.T) {
	manager := newTestManager(t)

	server, err := manager.CreateServer(CreateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword, Password: "secret123",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// PUT without password field -> keep existing password
	updated, err := manager.UpdateServer(server.ID, UpdateServerInput{
		Name: "localhost", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword,
		Tags: "prod",
	})
	if err != nil {
		t.Fatalf("update without password should succeed, got: %v", err)
	}
	if updated.Name != "localhost" || updated.Tags != "prod" {
		t.Fatalf("updated fields not persisted: %+v", updated)
	}
	if updated.PasswordEnc != server.PasswordEnc {
		t.Fatalf("password should be preserved, got %q want %q", updated.PasswordEnc, server.PasswordEnc)
	}
}

func TestUpdateServerRejectsEmptyPasswordWhenNoneStored(t *testing.T) {
	manager := newTestManager(t)

	// Create with key auth (no password stored)
	server, err := manager.CreateServer(CreateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthKey, KeyPath: "/root/.ssh/id_rsa",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Switch to password auth without providing a password and none stored -> error
	_, err = manager.UpdateServer(server.ID, UpdateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword,
	})
	if err == nil {
		t.Fatal("expected error when switching to password auth without password, got nil")
	}
}

func TestUpdateServerAcceptsNewPassword(t *testing.T) {
	manager := newTestManager(t)

	server, err := manager.CreateServer(CreateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword, Password: "oldpass",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := manager.UpdateServer(server.ID, UpdateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword, Password: "newpass",
	})
	if err != nil {
		t.Fatalf("update with new password: %v", err)
	}
	if updated.PasswordEnc == server.PasswordEnc {
		t.Fatal("password should be updated")
	}
}

func TestTestConnectionUsesStoredPasswordWhenOmitted(t *testing.T) {
	manager := newTestManager(t)

	server, err := manager.CreateServer(CreateServerInput{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword, Password: "secret123",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 不传密码：应回退到已保存密码，而不是直接报"密码不能为空"
	result, err := manager.TestConnection(server.ID, "")
	if err != nil {
		t.Fatalf("test connection without password should not fail on credential lookup, got: %v", err)
	}
	if result == nil {
		t.Fatal("expected probe result")
	}
	// 127.0.0.1:22 大概率不可达，但我们只关心错误来自 SSH/TCP 阶段而非密码缺失
	if result.Error == "SSH 配置无效: 密码不能为空" {
		t.Fatalf("probe should not fail on missing password when stored password exists, got: %s", result.Error)
	}
}

func TestTestConnectionFailsWhenNoStoredPassword(t *testing.T) {
	manager := newTestManager(t)

	// 直接构造一个 password 认证但无加密密码的记录（绕过 CreateServer 校验）
	server := &models.BastionServer{
		Name: "test", Host: "127.0.0.1", Port: 22,
		Username: "root", AuthMethod: models.BastionAuthPassword,
		Enabled: true,
	}
	if err := manager.db.Create(server).Error; err != nil {
		t.Fatalf("db create: %v", err)
	}

	_, err := manager.TestConnection(server.ID, "")
	if err == nil {
		t.Fatal("expected error when no stored password and none provided")
	}
}

func TestBuildSSHClientConfigRequiresKeyPathForKeyAuth(t *testing.T) {
	server := &models.BastionServer{
		Username: "root", AuthMethod: models.BastionAuthKey,
	}
	_, err := buildSSHClientConfig(server, "", 5*time.Second)
	if err == nil {
		t.Fatal("expected error for key auth without key path")
	}
}
