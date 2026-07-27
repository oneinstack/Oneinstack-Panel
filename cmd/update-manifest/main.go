package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"oneinstack/internal/services/panelupdate"
)

type artifactFlags map[string]string

func (a artifactFlags) String() string {
	return fmt.Sprint(map[string]string(a))
}

func (a artifactFlags) Set(value string) error {
	target, path, ok := strings.Cut(value, "=")
	if !ok || strings.TrimSpace(target) == "" || strings.TrimSpace(path) == "" {
		return fmt.Errorf("artifact must use TARGET=PATH")
	}
	if _, exists := a[target]; exists {
		return fmt.Errorf("duplicate artifact target %s", target)
	}
	a[target] = path
	return nil
}

func main() {
	var (
		version        = flag.String("version", "", "Release version, for example v0.4.0")
		channel        = flag.String("channel", "stable", "stable, beta, or development")
		minimumVersion = flag.String("minimum-version", "", "Oldest version accepted for direct upgrade")
		releaseNotes   = flag.String("release-notes", "", "Short release notes")
		baseURL        = flag.String("base-url", "", "HTTPS release asset base URL")
		keyID          = flag.String("key-id", "", "Trusted Ed25519 key ID")
		privateKeyFile = flag.String("private-key-file", "", "0600 file containing a base64 Ed25519 seed/private key")
		output         = flag.String("output", "manifest.json", "Output manifest path")
		publishedAt    = flag.String("published-at", "", "RFC3339 timestamp; defaults to current UTC time")
		artifacts      = make(artifactFlags)
	)
	flag.Var(artifacts, "artifact", "Release artifact TARGET=PATH; repeat for linux-amd64 and linux-arm64")
	flag.Parse()

	if err := run(*version, *channel, *minimumVersion, *releaseNotes, *baseURL, *keyID, *privateKeyFile, *output, *publishedAt, artifacts); err != nil {
		fmt.Fprintln(os.Stderr, "generate update manifest:", err)
		os.Exit(1)
	}
}

func run(version, channel, minimumVersion, releaseNotes, baseURL, keyID, privateKeyFile, output, publishedAt string, artifacts artifactFlags) error {
	if version == "" || baseURL == "" || keyID == "" || privateKeyFile == "" {
		return fmt.Errorf("version, base-url, key-id, and private-key-file are required")
	}
	if len(artifacts) == 0 {
		return fmt.Errorf("at least one artifact is required")
	}
	parsedBase, err := url.Parse(baseURL)
	if err != nil || parsedBase.Scheme != "https" || parsedBase.Host == "" {
		return fmt.Errorf("base-url must be an absolute HTTPS URL")
	}
	published := time.Now().UTC().Truncate(time.Second)
	if publishedAt != "" {
		published, err = time.Parse(time.RFC3339, publishedAt)
		if err != nil {
			return fmt.Errorf("parse published-at: %w", err)
		}
	}
	privateKey, err := readPrivateKey(privateKeyFile)
	if err != nil {
		return err
	}
	targets := make([]string, 0, len(artifacts))
	for target := range artifacts {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	manifest := panelupdate.Manifest{
		SchemaVersion: panelupdate.ManifestSchemaVersion,
		Version:       version, Channel: channel, PublishedAt: published,
		MinimumVersion: minimumVersion, ReleaseNotes: releaseNotes,
	}
	for _, target := range targets {
		osName, arch, ok := strings.Cut(target, "-")
		if !ok {
			return fmt.Errorf("invalid artifact target %q", target)
		}
		path := artifacts[target]
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() < 1 {
			return fmt.Errorf("artifact is not a non-empty regular file: %s", path)
		}
		digest, err := fileSHA256(path)
		if err != nil {
			return err
		}
		artifactURL, err := url.JoinPath(baseURL, filepath.Base(path))
		if err != nil {
			return err
		}
		manifest.Artifacts = append(manifest.Artifacts, panelupdate.Artifact{
			OS: osName, Arch: arch, URL: artifactURL,
			SHA256: digest, Size: info.Size(), FileName: filepath.Base(path),
		})
	}
	if err := panelupdate.SignManifest(&manifest, keyID, privateKey); err != nil {
		return err
	}
	// Verify the exact object before publishing it.
	publicKey := privateKey.Public().(ed25519.PublicKey)
	if _, err := panelupdate.VerifyManifest(manifest, panelupdate.Config{
		Channel: channel, TrustedKeys: map[string]string{
			keyID: base64.StdEncoding.EncodeToString(publicKey),
		},
		MaxPackageBytes: 2 << 30, OS: manifest.Artifacts[0].OS, Arch: manifest.Artifacts[0].Arch,
	}); err != nil {
		return fmt.Errorf("self-verify generated manifest: %w", err)
	}
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	return writeAtomic(output, content)
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("private key must be a regular file")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("private key file must not be accessible by group or other users")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(content)))
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	default:
		return nil, fmt.Errorf("private key must contain a %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".update-manifest-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0644); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
