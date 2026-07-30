package scriptregistry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"oneinstack/config"
)

func TestResolveRemoteVerifiesAndCachesPackage(t *testing.T) {
	archive := testPackageArchive(t)
	digest := sha256.Sum256(archive)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(publicKey)
	keyID := hex.EncodeToString(keyDigest[:8])
	metadata := Metadata{}
	if err := json.Unmarshal([]byte(testMetadataManifestJSON), &metadata.Manifest); err != nil {
		t.Fatal(err)
	}
	metadata.SHA256 = hex.EncodeToString(digest[:])
	metadata.Size = int64(len(archive))
	metadata.KeyID = keyID
	metadata.DownloadURL = "http://127.0.0.1:8189/download"
	metadata.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(
		privateKey,
		signingPayload("nginx", "1.0.0", metadata.SHA256, metadata.Size),
	))

	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/health/ready":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`{"status":"ok"}`))
		case "/v1/packages/resolve":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(metadata)
		case "/download":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(archive)
		default:
			http.NotFound(response, request)
		}
	})
	cachePath := t.TempDir()
	registry, err := New(config.ScriptCenter{
		Enabled:               true,
		URL:                   "http://127.0.0.1:8189",
		Channel:               "stable",
		RequestTimeoutSeconds: 5,
		MaxPackageBytes:       8 << 20,
		MaxExpandedBytes:      32 << 20,
		CachePath:             cachePath,
		BundledPath:           filepath.Join(t.TempDir(), "missing"),
		TrustedKeys: map[string]string{
			keyID: base64.StdEncoding.EncodeToString(publicKey),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.client.Transport = handlerTransport{handler: handler}

	resolved, err := registry.Resolve(context.Background(), "nginx", "1.26.2")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "remote" || resolved.Manifest.Component.Version != "1.0.0" {
		t.Fatalf("unexpected resolved package: %#v", resolved)
	}
	installAction, err := resolved.Action("install")
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(installAction); err != nil || info.Mode()&0111 == 0 {
		t.Fatalf("install action is not executable: info=%v err=%v", info, err)
	}

	resolvedAgain, err := registry.Resolve(context.Background(), "nginx", "1.26.2")
	if err != nil {
		t.Fatal(err)
	}
	if resolvedAgain.Source != "cache" {
		t.Fatalf("second resolution source = %q, want cache", resolvedAgain.Source)
	}
}

func TestResolveRejectsUntrustedSignature(t *testing.T) {
	archive := testPackageArchive(t)
	digest := sha256.Sum256(archive)
	var metadata Metadata
	if err := json.Unmarshal([]byte(testMetadataManifestJSON), &metadata.Manifest); err != nil {
		t.Fatal(err)
	}
	metadata.SHA256 = hex.EncodeToString(digest[:])
	metadata.Size = int64(len(archive))
	metadata.KeyID = "unknown"
	metadata.Signature = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	metadata.DownloadURL = "http://127.0.0.1:8189/download"
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/health/ready" {
			response.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(response).Encode(metadata)
	})
	registry, err := New(config.ScriptCenter{
		Enabled:               true,
		URL:                   "http://127.0.0.1:8189",
		Channel:               "stable",
		RequestTimeoutSeconds: 5,
		MaxPackageBytes:       8 << 20,
		MaxExpandedBytes:      32 << 20,
		CachePath:             t.TempDir(),
		BundledPath:           filepath.Join(t.TempDir(), "missing"),
		TrustedKeys:           map[string]string{"different": base64.StdEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))},
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.client.Transport = handlerTransport{handler: handler}
	if _, err := registry.Resolve(context.Background(), "nginx", "1.26.2"); err == nil {
		t.Fatal("untrusted package was accepted")
	}
}

func TestResolveInstalledReusesVerifiedPackageAcrossChannelChange(t *testing.T) {
	cachePath := t.TempDir()
	archive := testPackageArchive(t)
	archivePath := filepath.Join(t.TempDir(), "nginx.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	destination := filepath.Join(
		cachePath,
		"components",
		"nginx",
		"1.0.0",
		hex.EncodeToString(digest[:]),
	)
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := extractPackage(archivePath, destination, 32<<20); err != nil {
		t.Fatal(err)
	}
	registry, err := New(config.ScriptCenter{
		Enabled:     false,
		Channel:     "development",
		CachePath:   cachePath,
		BundledPath: filepath.Join(t.TempDir(), "missing"),
	})
	if err != nil {
		t.Fatal(err)
	}
	registry.host = Host{
		PanelVersion:  "v0.1.0-test",
		SystemID:      "ubuntu",
		SystemVersion: "24.04",
		Architecture:  "amd64",
	}

	if _, err := registry.Resolve(context.Background(), "nginx", "1.26.2"); err == nil {
		t.Fatal("new installation resolution crossed the configured channel")
	}
	resolved, err := registry.ResolveInstalled(
		context.Background(),
		"nginx",
		"1.26.2",
		"install",
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Source != "cache" ||
		resolved.Manifest.Component.Channel != "stable" ||
		resolved.Manifest.Component.Version != "1.0.0" {
		t.Fatalf("unexpected installed package resolution: %#v", resolved)
	}
	local, err := registry.ResolveInstalledLocal("nginx", "1.26.2", "install")
	if err != nil {
		t.Fatal(err)
	}
	if local.Source != "cache" || local.Root != resolved.Root {
		t.Fatalf("unexpected local installed package resolution: %#v", local)
	}
	if _, err := registry.ResolveInstalled(
		context.Background(),
		"nginx",
		"1.26.2",
		"configGet",
	); err == nil {
		t.Fatal("package missing a required action was selected")
	}
}

type handlerTransport struct {
	handler http.Handler
}

func (transport handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	transport.handler.ServeHTTP(recorder, request)
	return recorder.Result(), nil
}

func testPackageArchive(t *testing.T) []byte {
	t.Helper()
	manifest := []byte(`schemaVersion: 1
component:
  id: nginx
  name: Nginx
  version: 1.0.0
  softwareVersions: ["1.26.2"]
  channel: stable
compatibility:
  systems:
    - id: ubuntu
      versions: ["24.04"]
  architectures: [amd64]
actions:
  precheck: scripts/precheck.sh
  install: scripts/install.sh
  verify: scripts/verify.sh
  uninstall: scripts/uninstall.sh
`)
	script := []byte("#!/usr/bin/env bash\nset -Eeuo pipefail\n")
	scriptDigest := sha256.Sum256(script)
	checksums := []byte(
		hex.EncodeToString(scriptDigest[:]) + "  scripts/precheck.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/install.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/verify.sh\n" +
			hex.EncodeToString(scriptDigest[:]) + "  scripts/uninstall.sh\n",
	)
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	writeTarFile := func(name string, mode int64, contents []byte) {
		t.Helper()
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(contents)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	writeTarFile("manifest.yaml", 0644, manifest)
	for _, name := range []string{"scripts/precheck.sh", "scripts/install.sh", "scripts/verify.sh", "scripts/uninstall.sh"} {
		writeTarFile(name, 0755, script)
	}
	writeTarFile("files.sha256", 0644, checksums)
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

const testMetadataManifestJSON = `{
  "schemaVersion": 1,
  "component": {
    "id": "nginx",
    "name": "Nginx",
    "version": "1.0.0",
    "softwareVersions": ["1.26.2"],
    "channel": "stable"
  },
  "compatibility": {
    "systems": [{"id":"ubuntu","versions":["24.04"]}],
    "architectures": ["amd64"]
  },
  "actions": {
    "precheck": "scripts/precheck.sh",
    "install": "scripts/install.sh",
    "verify": "scripts/verify.sh",
    "uninstall": "scripts/uninstall.sh"
  }
}`
