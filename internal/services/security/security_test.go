package security

import (
	"bytes"
	"errors"
	"testing"
	"time"

	passwordcrypto "oneinstack/internal/crypto"
	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func securityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:" + t.Name() + "?mode=memory&cache=shared"))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(
		&models.User{}, &models.UserSession{}, &models.UserMFA{},
	); err != nil {
		t.Fatal(err)
	}
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x53}, 32)); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestPersistentSessionCanBeRevokedImmediately(t *testing.T) {
	database := securityTestDB(t)
	manager := NewSessionManager(database)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	record, err := manager.Create(NewSession{
		UserID: 5, Username: "operator", SecurityVersion: 3,
		RemoteIP: "192.0.2.20", UserAgent: "test", ExpiresAt: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Validate(record.ID, 5, 3); err != nil {
		t.Fatalf("valid session rejected: %v", err)
	}
	if revoked, err := manager.Revoke(5, record.ID, "test"); err != nil || !revoked {
		t.Fatalf("revoke: revoked=%v err=%v", revoked, err)
	}
	if _, err := manager.Validate(record.ID, 5, 3); !errors.Is(err, ErrSessionRevoked) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func TestTOTPEnrollmentAndOneTimeRecoveryCodes(t *testing.T) {
	database := securityTestDB(t)
	password := "R7!mQ2#vL9@xZ4"
	hash, err := passwordcrypto.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	user := models.User{
		ID: 7, Username: "secure-admin", Password: hash, IsAdmin: true,
		SecurityVersion: 1,
	}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	manager := NewTOTPManager(database)
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	setup, err := manager.Setup(user.ID, user.Username)
	if err != nil {
		t.Fatal(err)
	}
	if setup.Secret == "" || setup.OTPAuthURI == "" {
		t.Fatalf("incomplete setup: %#v", setup)
	}
	code, err := totpAt(setup.Secret, now.Unix()/totpPeriodSeconds)
	if err != nil {
		t.Fatal(err)
	}
	recoveryCodes, err := manager.Confirm(user.ID, password, code)
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveryCodes) != recoveryCodeCount {
		t.Fatalf("recovery codes = %d", len(recoveryCodes))
	}
	if err := manager.VerifyLoginCode(user.ID, code); err != nil {
		t.Fatalf("TOTP rejected: %v", err)
	}
	if err := manager.VerifyLoginCode(user.ID, code); !errors.Is(err, ErrInvalidSecondFactor) {
		t.Fatalf("replayed TOTP error = %v", err)
	}
	if err := manager.VerifyLoginCode(user.ID, recoveryCodes[0]); err != nil {
		t.Fatalf("recovery code rejected: %v", err)
	}
	if err := manager.VerifyLoginCode(user.ID, recoveryCodes[0]); !errors.Is(err, ErrInvalidSecondFactor) {
		t.Fatalf("reused recovery code error = %v", err)
	}
	status, err := manager.Status(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !status.TOTPEnabled || status.RecoveryCodesRemaining != recoveryCodeCount-1 {
		t.Fatalf("unexpected status: %#v", status)
	}

	var persisted models.UserMFA
	if err := database.First(&persisted, "user_id = ?", user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.SecretEncrypted == setup.Secret ||
		!utils.IsEncryptedCredential(persisted.SecretEncrypted) {
		t.Fatal("TOTP secret was not encrypted at rest")
	}
	for _, recovery := range recoveryCodes {
		if bytes.Contains([]byte(persisted.RecoveryCodeHashesJSON), []byte(recovery)) {
			t.Fatal("plaintext recovery code was persisted")
		}
	}
}
