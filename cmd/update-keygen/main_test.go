package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateCreatesRestrictedPrivateSeedAndPublicKey(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "private", "release.seed")
	publicPath := filepath.Join(root, "public", "release.pub")
	if err := generate("release-2026", privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	privateInfo, err := os.Stat(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	if privateInfo.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions = %04o, want 0600", privateInfo.Mode().Perm())
	}
	privateContent, err := os.ReadFile(privatePath)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(privateContent)))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("invalid private seed: len=%d err=%v", len(seed), err)
	}
	publicContent, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(publicContent)))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		t.Fatalf("invalid public key: len=%d err=%v", len(publicKey), err)
	}
	expected := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	if string(publicKey) != string(expected) {
		t.Fatal("public key does not match private seed")
	}
}

func TestGenerateRefusesToOverwriteExistingKey(t *testing.T) {
	root := t.TempDir()
	privatePath := filepath.Join(root, "release.seed")
	publicPath := filepath.Join(root, "release.pub")
	if err := os.WriteFile(privatePath, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := generate("release-2026", privatePath, publicPath); err == nil {
		t.Fatal("existing private key was overwritten")
	}
	content, err := os.ReadFile(privatePath)
	if err != nil || string(content) != "keep" {
		t.Fatalf("existing private key changed: %q err=%v", content, err)
	}
}
