package panelupdate

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
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeCommandRunner struct {
	mu       sync.Mutex
	commands []Command
	version  string
	failOn   string
}

func (f *fakeCommandRunner) Run(_ context.Context, command Command) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
	joined := command.Name + " " + strings.Join(command.Args, " ")
	if f.failOn != "" && strings.Contains(joined, f.failOn) {
		return nil, errors.New("simulated command failure")
	}
	if len(command.Args) == 1 && command.Args[0] == "version" {
		return []byte("Oneinstack Panel\nVersion: " + f.version + "\n"), nil
	}
	return []byte("ok"), nil
}

type fakeServiceController struct {
	active  bool
	stops   int
	starts  int
	onStart func(count int)
}

func (f *fakeServiceController) IsActive(context.Context) bool { return f.active }
func (f *fakeServiceController) Stop(context.Context) error {
	f.stops++
	f.active = false
	return nil
}
func (f *fakeServiceController) Start(context.Context) error {
	f.starts++
	f.active = true
	if f.onStart != nil {
		f.onStart(f.starts)
	}
	return nil
}

type fakeHealthChecker struct {
	results []error
	calls   int
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (f *fakeHealthChecker) WaitReady(context.Context, string, time.Duration) error {
	index := f.calls
	f.calls++
	if index < len(f.results) {
		return f.results[index]
	}
	return nil
}

func TestVerifyManifestRejectsTamperingAndUnknownJSON(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, privateKey, "http://127.0.0.1/artifact", []byte("release"))
	config := testUpdateConfig(t.TempDir(), publicKey)
	if _, err := VerifyManifest(manifest, config); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	manifest.Version = "v9.9.9"
	if _, err := VerifyManifest(manifest, config); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("tampered manifest was accepted: %v", err)
	}
	if _, err := DecodeManifest([]byte(`{"schemaVersion":1,"unknown":true}`)); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unknown manifest field was accepted: %v", err)
	}
}

func TestExtractReleaseRejectsTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{
		Name: "release/../../escaped", Mode: 0644, Size: 1, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	tarWriter.Close()
	gzipWriter.Close()
	file.Close()

	if _, err := extractRelease(archive, filepath.Join(t.TempDir(), "out"), 1<<20); err == nil {
		t.Fatal("path traversal archive was accepted")
	}
}

func TestExtractReleaseCountsFilesOutsideManagedPayloadAgainstLimit(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "oversized.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	files := []struct {
		name    string
		content string
		mode    int64
	}{
		{name: "release/ignored.bin", content: "12345", mode: 0644},
		{name: "release/one", content: "#!/bin/sh\n", mode: 0755},
		{name: "release/script-registry/bundled/marker", content: "scripts", mode: 0644},
	}
	for _, entry := range files {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(entry.content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(entry.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := extractRelease(archive, filepath.Join(t.TempDir(), "out"), 4); err == nil {
		t.Fatal("ignored archive content did not count against expanded size limit")
	}
}

func TestApplySwitchesReleaseAfterMigrationPreflight(t *testing.T) {
	fixture := newUpdateFixture(t)
	manager := fixture.manager
	status, err := manager.Apply(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateSucceeded || status.TargetVersion != "v1.1.0" {
		t.Fatalf("unexpected status: %#v", status)
	}
	target, err := os.Readlink(filepath.Join(fixture.installDir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join("releases", "1.1.0") {
		t.Fatalf("current target = %q", target)
	}
	binaryTarget, err := os.Readlink(filepath.Join(fixture.installDir, "one"))
	if err != nil {
		t.Fatal(err)
	}
	if binaryTarget != filepath.Join("current", "one") {
		t.Fatalf("binary symlink = %q", binaryTarget)
	}
	bundled, err := os.ReadFile(filepath.Join(fixture.installDir, "script-registry", "bundled", "marker"))
	if err != nil || string(bundled) != "new-scripts" {
		t.Fatalf("bundled scripts not switched: %q err=%v", bundled, err)
	}
	if fixture.runner.commandIndex("update preflight") < 0 {
		t.Fatalf("migration preflight was not executed: %#v", fixture.runner.commands)
	}
	if fixture.service.stops != 1 || fixture.service.starts != 1 || fixture.health.calls != 1 {
		t.Fatalf("unexpected lifecycle: stops=%d starts=%d health=%d", fixture.service.stops, fixture.service.starts, fixture.health.calls)
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "updates", "active-transaction.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful update left active journal: %v", err)
	}
}

func TestHealthFailureRestoresBinaryDatabaseAndBundledScripts(t *testing.T) {
	fixture := newUpdateFixture(t)
	fixture.health.results = []error{errors.New("new release unhealthy"), nil}
	fixture.service.onStart = func(count int) {
		if count == 1 {
			if err := os.WriteFile(filepath.Join(fixture.installDir, "myadmin.db"), []byte("migrated-db"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	status, err := fixture.manager.Apply(context.Background())
	if err == nil {
		t.Fatal("expected health check failure")
	}
	if status.State != StateRolledBack || !status.RollbackSucceeded {
		t.Fatalf("unexpected rollback status: %#v", status)
	}
	target, readErr := os.Readlink(filepath.Join(fixture.installDir, "current"))
	if readErr != nil || !strings.HasPrefix(target, filepath.Join("releases", "legacy-1.0.0-")) {
		t.Fatalf("old release pointer not restored: target=%q err=%v", target, readErr)
	}
	database, readErr := os.ReadFile(filepath.Join(fixture.installDir, "myadmin.db"))
	if readErr != nil || string(database) != "old-db" {
		t.Fatalf("database not restored: %q err=%v", database, readErr)
	}
	bundled, readErr := os.ReadFile(filepath.Join(fixture.installDir, "script-registry", "bundled", "marker"))
	if readErr != nil || string(bundled) != "old-scripts" {
		t.Fatalf("bundled scripts not restored: %q err=%v", bundled, readErr)
	}
	if fixture.service.starts != 2 || fixture.health.calls != 2 {
		t.Fatalf("old service was not restarted and checked: starts=%d health=%d", fixture.service.starts, fixture.health.calls)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.installDir, "releases", "1.1.0")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed candidate release was not removed: %v", statErr)
	}
	retryStatus, retryErr := fixture.manager.Apply(context.Background())
	if retryErr != nil || retryStatus.State != StateSucceeded {
		t.Fatalf("same version could not be retried after rollback: status=%#v err=%v", retryStatus, retryErr)
	}
}

func TestCandidatePreflightFailureRestartsOldServiceWithoutSwitch(t *testing.T) {
	fixture := newUpdateFixture(t)
	fixture.runner.failOn = "update preflight"
	status, err := fixture.manager.Apply(context.Background())
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if status.State != StateRolledBack {
		t.Fatalf("unexpected status: %#v", status)
	}
	if _, err := os.Lstat(filepath.Join(fixture.installDir, "current")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release pointer changed before successful preflight: %v", err)
	}
	if fixture.service.stops != 1 || fixture.service.starts != 1 {
		t.Fatalf("old service was not restored: stops=%d starts=%d", fixture.service.stops, fixture.service.starts)
	}
	if _, err := os.Stat(filepath.Join(fixture.installDir, "updates", "active-transaction.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful automatic rollback left active journal: %v", err)
	}
}

func TestRollbackLastRecoversInterruptedTransaction(t *testing.T) {
	fixture := newUpdateFixture(t)
	manager := fixture.manager
	operationID := "20260726T120000.000000000Z"
	fixture.service.active = false

	snapshot, err := manager.createSnapshot(operationID, true)
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(fixture.installDir, "updates", "staging", operationID, "payload")
	mustWriteFile(t, filepath.Join(payload, "one"), []byte("#!/bin/sh\nexit 0\n"), 0750)
	mustWriteFile(t, filepath.Join(payload, "script-registry", "bundled", "marker"), []byte("new-scripts"), 0640)
	releasePath, err := manager.releasePath("v1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.TargetReleasePath = releasePath
	if err := manager.writeJournal(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.promoteRelease(payload, releasePath, operationID); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(fixture.installDir, "current")
	releasesRoot := filepath.Join(fixture.installDir, "releases")
	previousTarget, err := manager.ensureReleaseLayout(currentPath, releasesRoot, operationID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.PreviousTarget = previousTarget
	if err := manager.writeJournal(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := atomicSymlink(filepath.Join("releases", "1.1.0"), currentPath, operationID); err != nil {
		t.Fatal(err)
	}
	oldBundled, bundledHadOld, err := manager.planBundledSwitch(operationID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.BundledSwitchPlan = true
	snapshot.BundledHadOld = bundledHadOld
	snapshot.OldBundledPath = oldBundled
	if err := manager.writeJournal(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.switchBundled(releasePath, operationID, bundledHadOld); err != nil {
		t.Fatal(err)
	}
	// Simulate a hard stop after the directory exchange but before the
	// "switched" journal update. The pre-recorded plan must still recover it.
	if err := os.WriteFile(filepath.Join(fixture.installDir, "myadmin.db"), []byte("migrated-db"), 0600); err != nil {
		t.Fatal(err)
	}

	status, err := manager.RollbackLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRolledBack || !status.RollbackSucceeded {
		t.Fatalf("unexpected rollback status: %#v", status)
	}
	target, err := os.Readlink(currentPath)
	if err != nil || target != previousTarget {
		t.Fatalf("release pointer was not restored: target=%q err=%v", target, err)
	}
	database, err := os.ReadFile(filepath.Join(fixture.installDir, "myadmin.db"))
	if err != nil || string(database) != "old-db" {
		t.Fatalf("database was not restored: %q err=%v", database, err)
	}
	bundled, err := os.ReadFile(filepath.Join(fixture.installDir, "script-registry", "bundled", "marker"))
	if err != nil || string(bundled) != "old-scripts" {
		t.Fatalf("bundled scripts were not restored: %q err=%v", bundled, err)
	}
	if _, err := os.Stat(releasePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted candidate release was not removed: %v", err)
	}
	if _, err := os.Stat(manager.journalPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("manual rollback left active journal: %v", err)
	}

	// A journal deletion failure must not make a second recovery attempt
	// destructive. Recreate the completed transaction marker and retry.
	snapshot.BundledSwitched = true
	if err := manager.writeJournal(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RollbackLast(context.Background()); err != nil {
		t.Fatalf("idempotent recovery retry failed: %v", err)
	}
}

func TestRollbackRecoversBundledSwitchWithoutPreviousDirectory(t *testing.T) {
	fixture := newUpdateFixture(t)
	manager := fixture.manager
	operationID := "20260726T123000.000000000Z"
	active := filepath.Join(fixture.installDir, "script-registry", "bundled")
	if err := os.RemoveAll(active); err != nil {
		t.Fatal(err)
	}
	fixture.service.active = false
	snapshot, err := manager.createSnapshot(operationID, false)
	if err != nil {
		t.Fatal(err)
	}
	releasePath := filepath.Join(fixture.installDir, "releases", "1.1.0")
	mustWriteFile(t, filepath.Join(releasePath, "script-registry", "bundled", "marker"), []byte("new-scripts"), 0640)
	oldBundled, bundledHadOld, err := manager.planBundledSwitch(operationID)
	if err != nil {
		t.Fatal(err)
	}
	if bundledHadOld {
		t.Fatal("test fixture unexpectedly has an old bundled directory")
	}
	snapshot.BundledSwitchPlan = true
	snapshot.BundledHadOld = false
	snapshot.OldBundledPath = oldBundled
	if err := manager.writeJournal(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := manager.switchBundled(releasePath, operationID, false); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.RollbackLast(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new bundled directory was not removed: %v", err)
	}
}

func TestApplyRequiresRecoveryWhenJournalExists(t *testing.T) {
	fixture := newUpdateFixture(t)
	if _, err := fixture.manager.createSnapshot("20260726T130000.000000000Z", true); err != nil {
		t.Fatal(err)
	}
	status, err := fixture.manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRecoveryNeeded {
		t.Fatalf("Status state = %q, want %q", status.State, StateRecoveryNeeded)
	}
	if _, err := fixture.manager.Apply(context.Background()); !errors.Is(err, ErrRecoveryNeeded) {
		t.Fatalf("Apply error = %v, want ErrRecoveryNeeded", err)
	}
}

func TestRollbackRecoversTransactionStartedBeforeServiceStop(t *testing.T) {
	fixture := newUpdateFixture(t)
	manager := fixture.manager
	if _, err := manager.beginSnapshot("20260726T140000.000000000Z", true); err != nil {
		t.Fatal(err)
	}
	status, err := manager.RollbackLast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateRolledBack {
		t.Fatalf("unexpected status: %#v", status)
	}
	if fixture.service.stops != 1 || fixture.service.starts != 1 {
		t.Fatalf("active old service was not safely restarted: stops=%d starts=%d", fixture.service.stops, fixture.service.starts)
	}
	database, err := os.ReadFile(filepath.Join(fixture.installDir, "myadmin.db"))
	if err != nil || string(database) != "old-db" {
		t.Fatalf("database changed during pre-stop recovery: %q err=%v", database, err)
	}
}

type updateFixture struct {
	installDir string
	manager    *Manager
	runner     *fakeCommandRunner
	service    *fakeServiceController
	health     *fakeHealthChecker
}

func newUpdateFixture(t *testing.T) updateFixture {
	t.Helper()
	installDir := t.TempDir()
	mustWriteFile(t, filepath.Join(installDir, "one"), []byte("#!/bin/sh\nexit 0\n"), 0750)
	mustWriteFile(t, filepath.Join(installDir, "config.yaml"), []byte("system:\n  port: 8089\n"), 0600)
	mustWriteFile(t, filepath.Join(installDir, "myadmin.db"), []byte("old-db"), 0600)
	mustWriteFile(t, filepath.Join(installDir, "script-registry", "bundled", "marker"), []byte("old-scripts"), 0640)

	archive := makeReleaseArchive(t)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifactURL := "https://updates.example.test/release.tar.gz"
	manifestURL := "https://updates.example.test/manifest.json"
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var content []byte
		contentType := "application/octet-stream"
		switch request.URL.Path {
		case "/release.tar.gz":
			content = archive
		case "/manifest.json":
			manifest := testManifest(t, privateKey, artifactURL, archive)
			content, _ = json.Marshal(manifest)
			contentType = "application/json"
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader("not found")),
				Header:     make(http.Header),
			}, nil
		}
		headers := make(http.Header)
		headers.Set("Content-Type", contentType)
		return &http.Response{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(content)),
			Header: headers, ContentLength: int64(len(content)),
		}, nil
	})}

	config := testUpdateConfig(installDir, publicKey)
	config.ManifestURL = manifestURL
	config.HealthURL = "http://127.0.0.1/health/ready"
	manager, err := NewManager(config)
	if err != nil {
		t.Fatal(err)
	}
	runner := &fakeCommandRunner{version: "v1.1.0"}
	service := &fakeServiceController{active: true}
	health := &fakeHealthChecker{}
	manager.runner = runner
	manager.service = service
	manager.health = health
	manager.client = client
	return updateFixture{
		installDir: installDir, manager: manager, runner: runner,
		service: service, health: health,
	}
}

func testUpdateConfig(installDir string, publicKey ed25519.PublicKey) Config {
	return Config{
		Enabled: true, ManifestURL: "http://127.0.0.1/manifest",
		Channel: "stable", TrustedKeys: map[string]string{
			"release-key": base64.StdEncoding.EncodeToString(publicKey),
		},
		RequestTimeout:  5 * time.Second,
		MaxPackageBytes: 2 << 20, MaxExpandedBytes: 4 << 20,
		HealthTimeout: time.Second, BackupRetention: 3,
		InstallDir: installDir, HealthURL: "http://127.0.0.1/health/ready",
		CurrentVersion: "v1.0.0", OS: "linux", Arch: "amd64",
	}
}

func testManifest(t *testing.T, privateKey ed25519.PrivateKey, artifactURL string, artifact []byte) Manifest {
	t.Helper()
	digest := sha256.Sum256(artifact)
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, Version: "v1.1.0",
		Channel: "stable", PublishedAt: time.Now().UTC().Truncate(time.Second),
		MinimumVersion: "v1.0.0", ReleaseNotes: "test release",
		Artifacts: []Artifact{{
			OS: "linux", Arch: "amd64", URL: artifactURL,
			SHA256: hex.EncodeToString(digest[:]), Size: int64(len(artifact)),
			FileName: "release.tar.gz",
		}},
	}
	if err := SignManifest(&manifest, "release-key", privateKey); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func makeReleaseArchive(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	files := []struct {
		name    string
		content string
		mode    int64
	}{
		{name: "one-linux-amd64-v1.1.0/one", content: "#!/bin/sh\nexit 0\n", mode: 0755},
		{name: "one-linux-amd64-v1.1.0/script-registry/bundled/marker", content: "new-scripts", mode: 0644},
	}
	for _, file := range files {
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: file.name, Mode: file.mode, Size: int64(len(file.content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write([]byte(file.content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func mustWriteFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeCommandRunner) commandIndex(fragment string) int {
	for index, command := range f.commands {
		if strings.Contains(command.Name+" "+strings.Join(command.Args, " "), fragment) {
			return index
		}
	}
	return -1
}
