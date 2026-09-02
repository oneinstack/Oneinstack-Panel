package translation

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oneinstack/internal/services/panelupdate"
)

const (
	defaultTranslationIdentityRelativePath   = "translation/panel-center.key"
	defaultTranslationActivationRelativePath = "translation/activation-code"
	defaultTranslationIdentityPath           = "/usr/local/one/translation/panel-center.key"
	defaultTranslationActivationPath         = "/usr/local/one/translation/activation-code"
)

type panelIdentity struct {
	instanceID string
	keyID      string
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

func loadPanelIdentity(installDir, identityPath string) (panelIdentity, bool, error) {
	instanceID, err := panelupdate.LoadOrCreateInstanceID(installDir)
	if err != nil {
		return panelIdentity{}, false, err
	}
	identityPath = resolveTranslationPath(installDir, identityPath, defaultTranslationIdentityPath, defaultTranslationIdentityRelativePath)
	privateKey, created, err := loadOrCreatePrivateKey(identityPath)
	if err != nil {
		return panelIdentity{}, false, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(publicKey)
	return panelIdentity{
		instanceID: instanceID,
		keyID:      hex.EncodeToString(digest[:8]),
		privateKey: privateKey,
		publicKey:  append(ed25519.PublicKey(nil), publicKey...),
	}, created, nil
}

func loadOrCreatePrivateKey(fileName string) (ed25519.PrivateKey, bool, error) {
	fileName = filepath.Clean(strings.TrimSpace(fileName))
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		return nil, false, errors.New("translation identity path is invalid")
	}
	parent := filepath.Dir(fileName)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, false, fmt.Errorf("create translation identity directory: %w", err)
	}
	if err := os.Chmod(parent, 0700); err != nil {
		return nil, false, fmt.Errorf("secure translation identity directory: %w", err)
	}
	privateKey, exists, err := readPrivateKey(fileName)
	if err != nil {
		return nil, false, err
	}
	if exists {
		return privateKey, false, nil
	}
	_, privateKey, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate translation identity: %w", err)
	}
	seed := privateKey.Seed()
	temporary, err := os.CreateTemp(parent, ".panel-center-key-*.tmp")
	if err != nil {
		return nil, false, fmt.Errorf("create translation identity: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return nil, false, fmt.Errorf("secure translation identity: %w", err)
	}
	if _, err := temporary.Write(seed); err != nil {
		_ = temporary.Close()
		return nil, false, fmt.Errorf("write translation identity: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return nil, false, fmt.Errorf("sync translation identity: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return nil, false, fmt.Errorf("close translation identity: %w", err)
	}
	if err := os.Link(temporaryName, fileName); err != nil {
		if os.IsExist(err) {
			privateKey, exists, readErr := readPrivateKey(fileName)
			if readErr != nil {
				return nil, false, readErr
			}
			if exists {
				return privateKey, false, nil
			}
		}
		return nil, false, fmt.Errorf("install translation identity: %w", err)
	}
	return privateKey, true, nil
}

func readPrivateKey(fileName string) (ed25519.PrivateKey, bool, error) {
	info, err := os.Lstat(fileName)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("translation identity must be a private regular file")
	}
	seed, err := os.ReadFile(fileName)
	if err != nil {
		return nil, false, fmt.Errorf("read translation identity: %w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, false, errors.New("translation identity has an invalid size")
	}
	return ed25519.NewKeyFromSeed(seed), true, nil
}

func readActivationCode(fileName string) (string, error) {
	fileName = filepath.Clean(strings.TrimSpace(fileName))
	if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
		return "", errors.New("translation activation-code path is invalid")
	}
	info, err := os.Lstat(fileName)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("translation activation code must be a private regular file")
	}
	code, err := os.ReadFile(fileName)
	if err != nil {
		return "", fmt.Errorf("read translation activation code: %w", err)
	}
	value := strings.TrimSpace(string(code))
	if value == "" || len(value) > 256 {
		return "", errors.New("translation activation code is empty or too long")
	}
	return value, nil
}

func removeActivationCode(fileName string) error {
	if err := os.Remove(fileName); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove consumed translation activation code: %w", err)
	}
	return nil
}

func publicKeyText(publicKey ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(publicKey)
}

func resolveTranslationPath(installDir, configured, defaultAbsolute, defaultRelative string) string {
	configured = filepath.Clean(strings.TrimSpace(configured))
	if configured == "" || configured == defaultAbsolute {
		return filepath.Join(installDir, defaultRelative)
	}
	return configured
}
