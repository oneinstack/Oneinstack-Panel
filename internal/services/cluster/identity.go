package cluster

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"oneinstack/app"
)

const addressChallengeDomain = "oneinstack-cluster-address-v1:"

var ErrInvalidAddressChallenge = errors.New("invalid address challenge")

func clusterIdentityPath() string {
	return filepath.Join(app.GetBasePath(), "cluster", "address-identity.key")
}

// The key lives outside release directories so Panel upgrades preserve it.
// Only the public half is sent during token-authenticated registration.
func loadClusterIdentity() (ed25519.PrivateKey, error) {
	path := clusterIdentityPath()
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	if key, exists, err := readClusterIdentity(path); err != nil || exists {
		return key, err
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(parent, ".address-identity-*.tmp")
	if err != nil {
		return nil, err
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return nil, err
	}
	if _, err := temporary.Write(key.Seed()); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return nil, err
	}
	if err := temporary.Close(); err != nil {
		return nil, err
	}
	if err := os.Link(temporary.Name(), path); err != nil {
		if os.IsExist(err) {
			stored, exists, readErr := readClusterIdentity(path)
			if readErr != nil {
				return nil, readErr
			}
			if !exists {
				return nil, errors.New("cluster identity creation was not completed")
			}
			return stored, nil
		}
		return nil, err
	}
	return key, nil
}

func readClusterIdentity(path string) (ed25519.PrivateKey, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, false, errors.New("cluster identity must be a private regular file")
	}
	seed, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, false, errors.New("cluster identity has an invalid size")
	}
	return ed25519.NewKeyFromSeed(seed), true, nil
}

func clusterPublicKeyText(key ed25519.PrivateKey) string {
	return base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey))
}

func validClusterPublicKey(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) > 64 {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	return err == nil && len(decoded) == ed25519.PublicKeySize && base64.StdEncoding.EncodeToString(decoded) == value
}

func addressChallengeMessage(nonce []byte) []byte {
	message := make([]byte, 0, len(addressChallengeDomain)+len(nonce))
	message = append(message, addressChallengeDomain...)
	return append(message, nonce...)
}

func SignAddressChallenge(nonceText string) (string, error) {
	nonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(nonceText))
	if err != nil || len(nonce) != 32 {
		return "", ErrInvalidAddressChallenge
	}
	key, err := loadClusterIdentity()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, addressChallengeMessage(nonce))), nil
}
