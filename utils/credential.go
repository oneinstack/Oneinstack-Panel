package utils

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const encryptedCredentialPrefix = "enc:v1:"

const (
	CredentialPurposeStoragePassword = "storage.password"
	CredentialPurposeLibraryPassword = "library.password"
	CredentialPurposeTOTPSecret      = "security.totp.secret"
	CredentialPurposeTOTPPending     = "security.totp.pending"
	CredentialPurposeRecoveryCode    = "security.recovery-code"
	CredentialPurposeNotification    = "monitor.notification-channel"
	CredentialPurposeBastionPassword = "bastion.password"
)

var (
	credentialMu     sync.RWMutex
	credentialCipher cipher.AEAD
	credentialKey    []byte
)

var ErrCredentialNotEncrypted = errors.New("credential is not encrypted")

// ConfigureCredentialKey installs the instance-local AES-256-GCM key.
func ConfigureCredentialKey(key []byte) error {
	if len(key) != 32 {
		return errors.New("credential key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(append([]byte(nil), key...))
	if err != nil {
		return err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	credentialMu.Lock()
	credentialCipher = aead
	credentialKey = append(credentialKey[:0], key...)
	credentialMu.Unlock()
	return nil
}

func CredentialCipherReady() bool {
	credentialMu.RLock()
	defer credentialMu.RUnlock()
	return credentialCipher != nil
}

// DeriveCredentialSubkey returns a purpose-scoped key without exposing the
// instance credential key itself.
func DeriveCredentialSubkey(purpose string) ([]byte, error) {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		return nil, errors.New("credential subkey purpose cannot be empty")
	}
	credentialMu.RLock()
	key := append([]byte(nil), credentialKey...)
	credentialMu.RUnlock()
	if len(key) != 32 {
		return nil, errors.New("credential encryption key is not configured")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("oneinstack:credential-subkey:v1:" + purpose))
	return mac.Sum(nil), nil
}

func IsEncryptedCredential(value string) bool {
	return strings.HasPrefix(value, encryptedCredentialPrefix)
}

func EncryptCredential(value, purpose string) (string, error) {
	if value == "" {
		return "", nil
	}
	credentialMu.RLock()
	aead := credentialCipher
	credentialMu.RUnlock()
	if aead == nil {
		return "", errors.New("credential encryption key is not configured")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate credential nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(value), []byte(purpose))
	payload := append(nonce, ciphertext...)
	return encryptedCredentialPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

func DecryptCredential(value, purpose string) (string, error) {
	if value == "" {
		return "", nil
	}
	if !IsEncryptedCredential(value) {
		return "", ErrCredentialNotEncrypted
	}
	credentialMu.RLock()
	aead := credentialCipher
	credentialMu.RUnlock()
	if aead == nil {
		return "", errors.New("credential encryption key is not configured")
	}
	payload, err := base64.RawURLEncoding.DecodeString(
		strings.TrimPrefix(value, encryptedCredentialPrefix),
	)
	if err != nil {
		return "", fmt.Errorf("decode encrypted credential: %w", err)
	}
	if len(payload) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("encrypted credential payload is truncated")
	}
	plaintext, err := aead.Open(
		nil,
		payload[:aead.NonceSize()],
		payload[aead.NonceSize():],
		[]byte(purpose),
	)
	if err != nil {
		return "", errors.New("decrypt credential: authentication failed")
	}
	return string(plaintext), nil
}
