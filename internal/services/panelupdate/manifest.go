package panelupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"oneinstack/internal/panelidentity"

	"golang.org/x/mod/semver"
)

type manifestPayload struct {
	SchemaVersion  int        `json:"schemaVersion"`
	Version        string     `json:"version"`
	Channel        string     `json:"channel"`
	PublishedAt    time.Time  `json:"publishedAt"`
	MinimumVersion string     `json:"minimumVersion,omitempty"`
	ReleaseNotes   string     `json:"releaseNotes,omitempty"`
	Artifacts      []Artifact `json:"artifacts"`
}

func ManifestPayload(manifest Manifest) ([]byte, error) {
	return json.Marshal(manifestPayload{
		SchemaVersion:  manifest.SchemaVersion,
		Version:        manifest.Version,
		Channel:        manifest.Channel,
		PublishedAt:    manifest.PublishedAt.UTC(),
		MinimumVersion: manifest.MinimumVersion,
		ReleaseNotes:   manifest.ReleaseNotes,
		Artifacts:      manifest.Artifacts,
	})
}

func SignManifest(manifest *Manifest, keyID string, privateKey ed25519.PrivateKey) error {
	if manifest == nil {
		return fmt.Errorf("%w: manifest is nil", ErrInvalidManifest)
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("Ed25519 private key must be %d bytes", ed25519.PrivateKeySize)
	}
	payload, err := ManifestPayload(*manifest)
	if err != nil {
		return err
	}
	manifest.Signature = ManifestSignature{
		KeyID: strings.TrimSpace(keyID),
		Value: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}
	return nil
}

func DecodeManifest(content []byte) (Manifest, error) {
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode JSON: %v", ErrInvalidManifest, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func VerifyManifest(manifest Manifest, config Config) (Artifact, error) {
	if manifest.SchemaVersion != ManifestSchemaVersion {
		return Artifact{}, fmt.Errorf("%w: unsupported schemaVersion %d", ErrInvalidManifest, manifest.SchemaVersion)
	}
	if !validVersion(manifest.Version) {
		return Artifact{}, fmt.Errorf("%w: invalid version %q", ErrInvalidManifest, manifest.Version)
	}
	switch manifest.Channel {
	case "stable", "beta", "development":
	default:
		return Artifact{}, fmt.Errorf("%w: invalid channel %q", ErrInvalidManifest, manifest.Channel)
	}
	if manifest.Channel != config.Channel {
		return Artifact{}, fmt.Errorf("%w: manifest channel %q does not match configured channel %q", ErrInvalidManifest, manifest.Channel, config.Channel)
	}
	if manifest.PublishedAt.IsZero() || manifest.PublishedAt.After(time.Now().UTC().Add(24*time.Hour)) {
		return Artifact{}, fmt.Errorf("%w: invalid publishedAt", ErrInvalidManifest)
	}
	if manifest.MinimumVersion != "" && !validVersion(manifest.MinimumVersion) {
		return Artifact{}, fmt.Errorf("%w: invalid minimumVersion", ErrInvalidManifest)
	}
	if manifest.MinimumVersion != "" &&
		semver.Compare(canonicalVersion(manifest.MinimumVersion), canonicalVersion(manifest.Version)) > 0 {
		return Artifact{}, fmt.Errorf("%w: minimumVersion is newer than the release", ErrInvalidManifest)
	}
	if len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > 16 {
		return Artifact{}, fmt.Errorf("%w: artifacts must contain 1-16 entries", ErrInvalidManifest)
	}
	keyID := strings.TrimSpace(manifest.Signature.KeyID)
	encodedKey, ok := config.TrustedKeys[keyID]
	if !ok || keyID == "" {
		return Artifact{}, fmt.Errorf("%w: untrusted signing key %q", ErrInvalidManifest, keyID)
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return Artifact{}, fmt.Errorf("%w: trusted key %q is not a valid Ed25519 key", ErrInvalidManifest, keyID)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(manifest.Signature.Value))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Artifact{}, fmt.Errorf("%w: invalid signature encoding", ErrInvalidManifest)
	}
	payload, err := ManifestPayload(manifest)
	if err != nil {
		return Artifact{}, err
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return Artifact{}, fmt.Errorf("%w: signature verification failed", ErrInvalidManifest)
	}

	var selected *Artifact
	seenTargets := make(map[string]struct{}, len(manifest.Artifacts))
	for index := range manifest.Artifacts {
		artifact := &manifest.Artifacts[index]
		target := artifact.OS + "/" + artifact.Arch
		if _, exists := seenTargets[target]; exists {
			return Artifact{}, fmt.Errorf("%w: duplicate artifact target %s", ErrInvalidManifest, target)
		}
		seenTargets[target] = struct{}{}
		if artifact.OS != "linux" || (artifact.Arch != "amd64" && artifact.Arch != "arm64") {
			return Artifact{}, fmt.Errorf("%w: unsupported artifact target %s", ErrInvalidManifest, target)
		}
		if artifact.Size < 1 || artifact.Size > config.MaxPackageBytes {
			return Artifact{}, fmt.Errorf("%w: artifact %s size is outside allowed range", ErrInvalidManifest, target)
		}
		digest, decodeErr := hex.DecodeString(artifact.SHA256)
		if decodeErr != nil || len(digest) != 32 || strings.ToLower(artifact.SHA256) != artifact.SHA256 {
			return Artifact{}, fmt.Errorf("%w: artifact %s has invalid SHA-256", ErrInvalidManifest, target)
		}
		if err := validateRemoteURL(artifact.URL); err != nil {
			return Artifact{}, fmt.Errorf("%w: artifact %s URL: %v", ErrInvalidManifest, target, err)
		}
		if strings.TrimSpace(artifact.FileName) == "" || strings.ContainsAny(artifact.FileName, `/\`) {
			return Artifact{}, fmt.Errorf("%w: artifact %s has invalid fileName", ErrInvalidManifest, target)
		}
		if artifact.OS == config.OS && artifact.Arch == config.Arch {
			copy := *artifact
			selected = &copy
		}
	}
	if selected == nil {
		return Artifact{}, fmt.Errorf("%w: no artifact for %s/%s", ErrInvalidManifest, config.OS, config.Arch)
	}
	return *selected, nil
}

func FetchManifest(ctx context.Context, client *http.Client, manifestURL string) (Manifest, error) {
	if err := validateRemoteURL(manifestURL); err != nil {
		return Manifest{}, fmt.Errorf("%w: manifest URL: %v", ErrInvalidManifest, err)
	}
	if client == nil {
		client = secureHTTPClient(20 * time.Second)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return Manifest{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return Manifest{}, fmt.Errorf("download update manifest: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("download update manifest: unexpected HTTP status %d", response.StatusCode)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read update manifest: %w", err)
	}
	if len(content) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalidManifest, MaxManifestBytes)
	}
	return DecodeManifest(content)
}

type centerResolveRequest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	CurrentVersion string `json:"currentVersion"`
	Channel        string `json:"channel"`
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	InstanceID     string `json:"instanceId"`
}

func ResolveManifest(
	ctx context.Context,
	client *http.Client,
	config Config,
) (Manifest, bool, error) {
	if err := validateRemoteURL(config.ResolveURL); err != nil {
		return Manifest{}, false, fmt.Errorf("%w: Center resolve URL: %v", ErrInvalidManifest, err)
	}
	if !instanceIDPattern.MatchString(config.InstanceID) {
		return Manifest{}, false, fmt.Errorf("%w: invalid panel instance ID", ErrInvalidManifest)
	}
	payload, err := json.Marshal(centerResolveRequest{
		SchemaVersion: ManifestSchemaVersion, CurrentVersion: config.CurrentVersion,
		Channel: config.Channel, OS: config.OS, Arch: config.Arch, InstanceID: config.InstanceID,
	})
	if err != nil {
		return Manifest{}, false, fmt.Errorf("encode Center update request: %w", err)
	}
	if client == nil {
		client = secureHTTPClient(20 * time.Second)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, config.ResolveURL, bytes.NewReader(payload))
	if err != nil {
		return Manifest{}, false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if networkInfo := panelidentity.HeaderValue(); networkInfo != "" {
		request.Header.Set(panelidentity.NetworkInfoHeader, networkInfo)
	}
	response, err := client.Do(request)
	if err != nil {
		return Manifest{}, false, fmt.Errorf("resolve panel update from Center: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return Manifest{}, false, nil
	}
	if response.StatusCode != http.StatusOK {
		return Manifest{}, false, fmt.Errorf(
			"resolve panel update from Center: unexpected HTTP status %d",
			response.StatusCode,
		)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, MaxManifestBytes+1))
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read Center update manifest: %w", err)
	}
	if len(content) > MaxManifestBytes {
		return Manifest{}, false, fmt.Errorf("%w: manifest exceeds %d bytes", ErrInvalidManifest, MaxManifestBytes)
	}
	manifest, err := DecodeManifest(content)
	if err != nil {
		return Manifest{}, false, err
	}
	return manifest, true, nil
}

func CheckUpdate(ctx context.Context, client *http.Client, config Config) (CheckResult, Manifest, Artifact, error) {
	result := CheckResult{
		Enabled: config.Enabled, CurrentVersion: config.CurrentVersion,
		Channel: config.Channel, Compatible: true, InstanceID: config.InstanceID,
	}
	if !config.Enabled {
		return result, Manifest{}, Artifact{}, ErrDisabled
	}
	current := canonicalVersion(config.CurrentVersion)
	if current == "" {
		result.Compatible = false
		return result, Manifest{}, Artifact{}, fmt.Errorf(
			"%w: current build version %q is not a release version",
			ErrInvalidManifest,
			config.CurrentVersion,
		)
	}
	trustedKeys, trust, err := RefreshKeyTrust(ctx, client, config)
	if err != nil {
		return result, Manifest{}, Artifact{}, err
	}
	config.TrustedKeys = trustedKeys
	result.TrustRevision = trust.Revision
	result.TrustSource = trust.Source
	result.TrustedKeyCount = trust.TrustedKeyCount
	result.RevokedKeyCount = trust.RevokedKeyCount
	result.TrustUpdatedAt = trust.UpdatedAt
	var (
		manifest   Manifest
		found      = true
		resolveErr error
	)
	if strings.TrimSpace(config.ResolveURL) != "" {
		result.Source = "center"
		manifest, found, resolveErr = ResolveManifest(ctx, client, config)
	} else {
		result.Source = "manifest"
		manifest, resolveErr = FetchManifest(ctx, client, config.ManifestURL)
	}
	if resolveErr != nil {
		return result, Manifest{}, Artifact{}, resolveErr
	}
	if !found {
		result.LatestVersion = config.CurrentVersion
		return result, Manifest{}, Artifact{}, nil
	}
	artifact, err := VerifyManifest(manifest, config)
	if err != nil {
		return result, Manifest{}, Artifact{}, err
	}
	result.LatestVersion = manifest.Version
	result.PublishedAt = manifest.PublishedAt
	result.ReleaseNotes = manifest.ReleaseNotes
	result.MinimumVersion = manifest.MinimumVersion
	result.ArtifactSize = artifact.Size
	result.SigningKeyID = manifest.Signature.KeyID
	if manifest.MinimumVersion != "" && semver.Compare(current, canonicalVersion(manifest.MinimumVersion)) < 0 {
		result.Compatible = false
		return result, manifest, artifact, fmt.Errorf("%w: current version is below minimum upgrade version %s", ErrInvalidManifest, manifest.MinimumVersion)
	}
	result.UpdateAvailable = semver.Compare(canonicalVersion(manifest.Version), current) > 0
	return result, manifest, artifact, nil
}

func canonicalVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return ""
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	if !semver.IsValid(version) {
		return ""
	}
	return version
}

func validVersion(version string) bool {
	return canonicalVersion(version) != ""
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values are not allowed", ErrInvalidManifest)
		}
		return fmt.Errorf("%w: trailing JSON: %v", ErrInvalidManifest, err)
	}
	return nil
}

func validateRemoteURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("must be an absolute URL")
	}
	if parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("credentials and fragments are not allowed")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	if parsed.Scheme == "http" && isLoopback(parsed.Hostname()) {
		return nil
	}
	return fmt.Errorf("HTTPS is required (HTTP is allowed only for loopback testing)")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func secureHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return fmt.Errorf("too many redirects")
		}
		if err := validateRemoteURL(request.URL.String()); err != nil {
			return err
		}
		if len(via) > 0 && !strings.EqualFold(request.URL.Hostname(), via[0].URL.Hostname()) {
			return fmt.Errorf("cross-host redirect is not allowed")
		}
		return nil
	}
	return client
}
