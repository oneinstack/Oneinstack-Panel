package panelupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCheckUpdateResolvesSignedManifestFromCenter(t *testing.T) {
	t.Parallel()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var received centerResolveRequest
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/panel/releases/resolve" {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
			}, nil
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			return nil, err
		}
		release := []byte("center-controlled-release")
		manifest := testManifest(t, privateKey, "http://127.0.0.1/artifact", release)
		content, err := json.Marshal(manifest)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(content)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	})}

	config := testUpdateConfig(t.TempDir(), publicKey)
	config.ManifestURL = ""
	config.ResolveURL = "http://127.0.0.1/v1/panel/releases/resolve"
	config.InstanceID = "panel-0123456789abcdef0123456789abcdef"
	result, resolved, artifact, err := CheckUpdate(context.Background(), client, config)
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.Source != "center" ||
		result.LatestVersion != "v1.1.0" || resolved.Version != "v1.1.0" ||
		artifact.OS != "linux" || artifact.Arch != "amd64" {
		t.Fatalf("unexpected Center result: result=%+v manifest=%+v artifact=%+v", result, resolved, artifact)
	}
	if received.SchemaVersion != ManifestSchemaVersion ||
		received.CurrentVersion != "v1.0.0" ||
		received.Channel != "stable" ||
		received.OS != "linux" ||
		received.Arch != "amd64" ||
		received.InstanceID != config.InstanceID {
		t.Fatalf("unexpected Center resolve request: %+v", received)
	}
}

func TestCheckUpdateHandlesCenterNoContent(t *testing.T) {
	t.Parallel()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(strings.NewReader("")),
			Header:     make(http.Header),
		}, nil
	})}
	config := testUpdateConfig(t.TempDir(), publicKey)
	config.ManifestURL = ""
	config.ResolveURL = "http://127.0.0.1/v1/panel/releases/resolve"
	config.InstanceID = "panel-0123456789abcdef0123456789abcdef"
	result, _, _, err := CheckUpdate(context.Background(), client, config)
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable || result.LatestVersion != config.CurrentVersion || result.Source != "center" {
		t.Fatalf("unexpected no-update result: %+v", result)
	}
}

func TestLoadOrCreateInstanceIDIsStableAndPrivate(t *testing.T) {
	t.Parallel()
	installDir := t.TempDir()
	first, err := LoadOrCreateInstanceID(installDir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateInstanceID(installDir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !instanceIDPattern.MatchString(first) {
		t.Fatalf("instance ID is not stable/valid: first=%q second=%q", first, second)
	}
	info, err := os.Stat(filepath.Join(installDir, "panel-instance-id"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("instance ID permissions = %o", info.Mode().Perm())
	}
}

func TestLoadOrCreateInstanceIDIsStableDuringConcurrentCreation(t *testing.T) {
	t.Parallel()
	installDir := t.TempDir()
	const workers = 16
	results := make(chan string, workers)
	errors := make(chan error, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			instanceID, err := LoadOrCreateInstanceID(installDir)
			if err != nil {
				errors <- err
				return
			}
			results <- instanceID
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	expected := ""
	for instanceID := range results {
		if expected == "" {
			expected = instanceID
		}
		if instanceID != expected {
			t.Fatalf("concurrent instance IDs differ: %q and %q", expected, instanceID)
		}
	}
}

func TestLoadOrCreateInstanceIDRejectsUnsafeStoredFile(t *testing.T) {
	t.Parallel()
	installDir := t.TempDir()
	fileName := filepath.Join(installDir, "panel-instance-id")
	if err := os.WriteFile(fileName, []byte("panel-0123456789abcdef0123456789abcdef\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fileName, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateInstanceID(installDir); err == nil {
		t.Fatal("world-readable instance ID file was accepted")
	}
}

func TestVerifyCenterManifestUsesPinnedKey(t *testing.T) {
	t.Parallel()
	trustedPublic, trustedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, untrustedPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, untrustedPrivate, "http://127.0.0.1/artifact", []byte("release"))
	config := testUpdateConfig(t.TempDir(), trustedPublic)
	config.TrustedKeys = map[string]string{
		"release-key": base64.StdEncoding.EncodeToString(trustedPublic),
	}
	if _, err := VerifyManifest(manifest, config); err == nil {
		t.Fatal("manifest signed by an untrusted Center key was accepted")
	}
	manifest = testManifest(t, trustedPrivate, "http://127.0.0.1/artifact", []byte("release"))
	if _, err := VerifyManifest(manifest, config); err != nil {
		t.Fatalf("manifest signed by pinned Center key was rejected: %v", err)
	}
}
