package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"oneinstack/internal/services/panelupdate"
)

func TestRunGeneratesVerifiableManifest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	keyPath := filepath.Join(root, "private.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(privateKey.Seed())), 0600); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(root, "one-linux-amd64-v1.2.3.tar.gz")
	if err := os.WriteFile(artifactPath, []byte("signed release artifact"), 0644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "manifest.json")
	if err := run(
		"v1.2.3", "stable", "v1.0.0", "release notes",
		"https://updates.example.com/v1.2.3", "release-key", keyPath, output,
		"2026-07-26T00:00:00Z",
		artifactFlags{"linux-amd64": artifactPath},
	); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var manifest panelupdate.Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatal(err)
	}
	artifact, err := panelupdate.VerifyManifest(manifest, panelupdate.Config{
		Channel: "stable", TrustedKeys: map[string]string{
			"release-key": base64.StdEncoding.EncodeToString(publicKey),
		},
		MaxPackageBytes: 1 << 20, OS: "linux", Arch: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.FileName != filepath.Base(artifactPath) || manifest.Version != "v1.2.3" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestReadPrivateKeyRejectsPermissiveFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(make([]byte, 32))), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKey(path); err == nil {
		t.Fatal("permissive private key file was accepted")
	}
}
