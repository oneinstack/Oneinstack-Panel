package utils

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestCredentialEncryptionUsesAuthenticatedRandomNonces(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	if err := ConfigureCredentialKey(key); err != nil {
		t.Fatal(err)
	}
	first, err := EncryptCredential("database-secret", CredentialPurposeStoragePassword)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncryptCredential("database-secret", CredentialPurposeStoragePassword)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !IsEncryptedCredential(first) ||
		strings.Contains(first, "database-secret") {
		t.Fatalf("credential ciphertext is unsafe: first=%q second=%q", first, second)
	}
	plaintext, err := DecryptCredential(first, CredentialPurposeStoragePassword)
	if err != nil {
		t.Fatal(err)
	}
	if plaintext != "database-secret" {
		t.Fatalf("decrypted credential = %q", plaintext)
	}
	if _, err := DecryptCredential(first, CredentialPurposeLibraryPassword); err == nil {
		t.Fatal("credential decrypted with the wrong purpose")
	}
	if _, err := DecryptCredential("database-secret", CredentialPurposeStoragePassword); !errors.Is(err, ErrCredentialNotEncrypted) {
		t.Fatalf("plaintext credential error = %v", err)
	}
}

func TestCredentialSubkeysArePurposeScoped(t *testing.T) {
	if err := ConfigureCredentialKey(bytes.Repeat([]byte{0x29}, 32)); err != nil {
		t.Fatal(err)
	}
	auditKey, err := DeriveCredentialSubkey("audit")
	if err != nil {
		t.Fatal(err)
	}
	storageKey, err := DeriveCredentialSubkey("storage")
	if err != nil {
		t.Fatal(err)
	}
	if len(auditKey) != 32 || bytes.Equal(auditKey, storageKey) {
		t.Fatal("credential subkeys are not safely purpose-scoped")
	}
}
