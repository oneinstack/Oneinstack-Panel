package scriptregistry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"oneinstack/config"
	"oneinstack/internal/buildinfo"
)

type Registry struct {
	config  config.ScriptCenter
	client  *http.Client
	baseURL *url.URL
	host    Host
}

var ErrBatchUnsupported = errors.New("script center batch endpoint is unsupported")

const packageBatchLimit = 512

type PackageBatchRequest struct {
	Component       string `json:"component"`
	SoftwareVersion string `json:"softwareVersion"`
	Channel         string `json:"channel"`
	Host            Host   `json:"host,omitempty"`
}

type packageBatchResolveResponse struct {
	Results []struct {
		Component       string   `json:"component"`
		SoftwareVersion string   `json:"softwareVersion"`
		Channel         string   `json:"channel"`
		Metadata        Metadata `json:"metadata"`
		ErrorCode       string   `json:"errorCode"`
	} `json:"results"`
}

type packageBatchAvailableResponse struct {
	Results []struct {
		Component       string `json:"component"`
		SoftwareVersion string `json:"softwareVersion"`
		Channel         string `json:"channel"`
		Available       bool   `json:"available"`
	} `json:"results"`
}

func New(centerConfig config.ScriptCenter) (*Registry, error) {
	var baseURL *url.URL
	var err error
	if centerConfig.Enabled {
		baseURL, err = url.Parse(strings.TrimRight(centerConfig.URL, "/"))
		if err != nil {
			return nil, fmt.Errorf("parse script center URL: %w", err)
		}
	}
	return &Registry{
		config: centerConfig,
		client: &http.Client{
			Timeout: time.Duration(centerConfig.RequestTimeoutSeconds) * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		baseURL: baseURL,
		host:    detectHost(),
	}, nil
}

func (r *Registry) Resolve(ctx context.Context, component, softwareVersion string) (Package, error) {
	var remoteErr error
	if r.config.Enabled {
		pkg, err := r.resolveRemote(ctx, component, softwareVersion)
		if err == nil {
			return pkg, nil
		}
		remoteErr = err
	}
	pkg, bundledErr := r.resolveBundled(component, softwareVersion)
	if bundledErr == nil {
		return pkg, nil
	}
	if remoteErr != nil {
		return Package{}, fmt.Errorf("remote package unavailable (%v); bundled package unavailable: %w", remoteErr, bundledErr)
	}
	return Package{}, bundledErr
}

// ResolveChannel resolves a new installation from the channel assigned to the
// exact software version by the signed Center catalog. A Panel may retain rows
// from more than one catalog channel in its offline snapshot, so using only the
// globally configured channel can make an otherwise published version appear
// unavailable.
func (r *Registry) ResolveChannel(
	ctx context.Context,
	component string,
	softwareVersion string,
	channel string,
) (Package, error) {
	channel = strings.ToLower(strings.TrimSpace(channel))
	if channel == "" || channel == r.config.Channel {
		return r.Resolve(ctx, component, softwareVersion)
	}
	switch channel {
	case "stable", "beta", "development":
		selected := *r
		selected.config.Channel = channel
		return selected.Resolve(ctx, component, softwareVersion)
	default:
		return Package{}, fmt.Errorf("invalid package channel %q", channel)
	}
}

// ResolvePackageVersionChannel returns the newest signed package version that
// Center resolves for this Panel host without downloading or extracting the
// package. Catalog synchronization uses it to detect component-package
// upgrades independently from the software version.
func (r *Registry) ResolvePackageVersionChannel(
	ctx context.Context,
	component string,
	softwareVersion string,
	channel string,
) (string, error) {
	if !r.config.Enabled {
		return "", errors.New("script center is disabled")
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	selected := *r
	if channel != "" {
		switch channel {
		case "stable", "beta", "development":
			selected.config.Channel = channel
		default:
			return "", fmt.Errorf("invalid package channel %q", channel)
		}
	}
	metadata, err := selected.resolveRemoteMetadata(ctx, component, softwareVersion)
	if err != nil {
		return "", err
	}
	return metadata.Manifest.Component.Version, nil
}

// PackageAvailableChannel reports whether Center has a published package for
// the requested software version, without applying this host's compatibility.
func (r *Registry) PackageAvailableChannel(
	ctx context.Context,
	component string,
	softwareVersion string,
	channel string,
) (bool, error) {
	if !r.config.Enabled {
		return false, errors.New("script center is disabled")
	}
	channel = strings.ToLower(strings.TrimSpace(channel))
	switch channel {
	case "stable", "beta", "development":
	default:
		return false, fmt.Errorf("invalid package channel %q", channel)
	}
	body, err := json.Marshal(struct {
		Component       string `json:"component"`
		SoftwareVersion string `json:"softwareVersion"`
		Channel         string `json:"channel"`
	}{component, softwareVersion, channel})
	if err != nil {
		return false, fmt.Errorf("encode package availability request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint("/v1/packages/available"), bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("create package availability request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return false, fmt.Errorf("check package availability: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, decodeAPIError(response)
	}
	var result struct {
		Available bool `json:"available"`
	}
	if err := decodeLimitedJSON(response.Body, &result); err != nil {
		return false, fmt.Errorf("decode package availability: %w", err)
	}
	return result.Available, nil
}

func (r *Registry) PackageAvailableChannelBatch(ctx context.Context, requests []PackageBatchRequest) (map[string]bool, error) {
	if !r.config.Enabled {
		return nil, errors.New("script center is disabled")
	}
	if len(requests) == 0 {
		return map[string]bool{}, nil
	}
	available := make(map[string]bool, len(requests))
	for start := 0; start < len(requests); start += packageBatchLimit {
		end := start + packageBatchLimit
		if end > len(requests) {
			end = len(requests)
		}
		part, err := r.packageAvailableChannelBatchOnce(ctx, requests[start:end])
		if err != nil {
			return nil, err
		}
		for key, value := range part {
			available[key] = value
		}
	}
	return available, nil
}

func (r *Registry) packageAvailableChannelBatchOnce(ctx context.Context, requests []PackageBatchRequest) (map[string]bool, error) {
	log.Printf("software catalog: Center package availability batch requests=%d", len(requests))
	wireRequests := make([]struct {
		Component       string `json:"component"`
		SoftwareVersion string `json:"softwareVersion"`
		Channel         string `json:"channel"`
	}, 0, len(requests))
	for _, item := range requests {
		wireRequests = append(wireRequests, struct {
			Component       string `json:"component"`
			SoftwareVersion string `json:"softwareVersion"`
			Channel         string `json:"channel"`
		}{
			Component: item.Component, SoftwareVersion: item.SoftwareVersion, Channel: item.Channel,
		})
	}
	body, err := json.Marshal(struct {
		Requests any `json:"requests"`
	}{wireRequests})
	if err != nil {
		return nil, fmt.Errorf("encode package availability batch request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint("/v1/packages/available/batch"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create package availability batch request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("check package availability batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrBatchUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, decodeAPIError(response)
	}
	var result packageBatchAvailableResponse
	if err := decodeLimitedJSON(response.Body, &result); err != nil {
		return nil, fmt.Errorf("decode package availability batch: %w", err)
	}
	available := make(map[string]bool, len(result.Results))
	for _, item := range result.Results {
		available[batchKey(item.Component, item.SoftwareVersion, item.Channel)] = item.Available
	}
	return available, nil
}

func (r *Registry) ResolvePackageVersionChannelBatch(ctx context.Context, requests []PackageBatchRequest) (map[string]string, error) {
	if !r.config.Enabled {
		return nil, errors.New("script center is disabled")
	}
	if len(requests) == 0 {
		return map[string]string{}, nil
	}
	versions := make(map[string]string, len(requests))
	for start := 0; start < len(requests); start += packageBatchLimit {
		end := start + packageBatchLimit
		if end > len(requests) {
			end = len(requests)
		}
		part, err := r.resolvePackageVersionChannelBatchOnce(ctx, requests[start:end])
		if err != nil {
			return nil, err
		}
		for key, value := range part {
			versions[key] = value
		}
	}
	return versions, nil
}

func (r *Registry) resolvePackageVersionChannelBatchOnce(ctx context.Context, requests []PackageBatchRequest) (map[string]string, error) {
	log.Printf("software catalog: Center package resolve batch requests=%d", len(requests))
	for index := range requests {
		requests[index].Host = r.host
	}
	body, err := json.Marshal(struct {
		Requests []PackageBatchRequest `json:"requests"`
	}{requests})
	if err != nil {
		return nil, fmt.Errorf("encode package resolve batch request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint("/v1/packages/resolve/batch"), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create package resolve batch request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("resolve package batch: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrBatchUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return nil, decodeAPIError(response)
	}
	var result packageBatchResolveResponse
	if err := decodeLimitedJSON(response.Body, &result); err != nil {
		return nil, fmt.Errorf("decode package resolve batch: %w", err)
	}
	versions := make(map[string]string, len(result.Results))
	for _, item := range result.Results {
		if item.ErrorCode != "" || item.Metadata.Manifest.Component.Version == "" {
			continue
		}
		if err := item.Metadata.Manifest.validate(); err != nil || item.Metadata.Manifest.Component.ID != item.Component ||
			!item.Metadata.Manifest.supportsSoftwareVersion(item.SoftwareVersion) || item.Metadata.Manifest.Component.Channel != item.Channel {
			continue
		}
		if err := r.verifyMetadata(item.Metadata); err != nil {
			continue
		}
		versions[batchKey(item.Component, item.SoftwareVersion, item.Channel)] = item.Metadata.Manifest.Component.Version
	}
	return versions, nil
}

func batchKey(component, softwareVersion, channel string) string {
	return component + "\x00" + softwareVersion + "\x00" + channel
}

// ResolveInstalled resolves a package for an already installed software
// version. New installations remain pinned to the configured channel through
// Resolve, while lifecycle and configuration operations may reuse a verified
// package from the original channel after the marketplace channel changes.
func (r *Registry) ResolveInstalled(
	ctx context.Context,
	component string,
	softwareVersion string,
	requiredActions ...string,
) (Package, error) {
	current, currentErr := r.Resolve(ctx, component, softwareVersion)
	if currentErr == nil && packageSupportsActions(current.Manifest, requiredActions) {
		return current, nil
	}
	if currentErr == nil {
		currentErr = fmt.Errorf("configured-channel package does not provide the required actions")
	}

	cached, cacheErr := r.resolveCachedInstalled(component, softwareVersion, requiredActions)
	if cacheErr == nil {
		return cached, nil
	}

	var fallbackErr error
	if r.config.Enabled {
		for _, channel := range []string{"stable", "beta", "development"} {
			if channel == r.config.Channel {
				continue
			}
			fallback := *r
			fallback.config.Channel = channel
			candidate, err := fallback.resolveRemote(ctx, component, softwareVersion)
			if err == nil && packageSupportsActions(candidate.Manifest, requiredActions) {
				return candidate, nil
			}
			if err == nil {
				err = fmt.Errorf("package does not provide the required actions")
			}
			fallbackErr = errors.Join(fallbackErr, fmt.Errorf("%s: %w", channel, err))
		}
	}

	bundled, bundledErr := r.resolveBundledInstalled(component, softwareVersion, requiredActions)
	if bundledErr == nil {
		return bundled, nil
	}
	return Package{}, fmt.Errorf(
		"no compatible package for installed %s %s (configured channel: %v; cache: %v; fallback channels: %v; bundled: %v)",
		component,
		softwareVersion,
		currentErr,
		cacheErr,
		fallbackErr,
		bundledErr,
	)
}

// ResolveInstalledUninstall resolves the uninstall action for an installed
// component. Uninstall must remain possible after a host upgrade or a Center
// package is retired, so a previously verified local package may be reused
// without re-evaluating its installation-time host compatibility.
func (r *Registry) ResolveInstalledUninstall(
	ctx context.Context,
	component string,
	softwareVersion string,
) (Package, error) {
	resolved, err := r.ResolveInstalled(ctx, component, softwareVersion, "uninstall")
	if err == nil {
		return resolved, nil
	}

	cached, cacheErr := r.resolveCachedInstalled(component, softwareVersion, []string{"uninstall"}, false)
	if cacheErr == nil {
		return cached, nil
	}
	bundled, bundledErr := r.resolveBundledInstalled(component, softwareVersion, []string{"uninstall"}, false)
	if bundledErr == nil {
		return bundled, nil
	}
	return Package{}, fmt.Errorf(
		"no verified uninstall package for installed %s %s (lifecycle: %v; cache: %v; bundled: %v)",
		component,
		softwareVersion,
		err,
		cacheErr,
		bundledErr,
	)
}

// ResolveInstalledLocal resolves an already verified installed-component
// package without contacting Center. It is intended for frequent local health
// probes where a temporary Center outage must not make a running service look
// unhealthy. Installation, upgrade, and user-triggered lifecycle operations
// continue to use Resolve or ResolveInstalled.
func (r *Registry) ResolveInstalledLocal(
	component string,
	softwareVersion string,
	requiredActions ...string,
) (Package, error) {
	cached, cacheErr := r.resolveCachedInstalled(component, softwareVersion, requiredActions)
	if cacheErr == nil {
		return cached, nil
	}
	bundled, bundledErr := r.resolveBundledInstalled(component, softwareVersion, requiredActions)
	if bundledErr == nil {
		return bundled, nil
	}
	return Package{}, fmt.Errorf(
		"no compatible local package for installed %s %s (cache: %v; bundled: %v)",
		component,
		softwareVersion,
		cacheErr,
		bundledErr,
	)
}

func (r *Registry) resolveCachedInstalled(
	component string,
	softwareVersion string,
	requiredActions []string,
	compatibilityRequired ...bool,
) (Package, error) {
	checkCompatibility := true
	if len(compatibilityRequired) > 0 {
		checkCompatibility = compatibilityRequired[0]
	}
	componentRoot := filepath.Join(r.config.CachePath, "components", component)
	versions, err := os.ReadDir(componentRoot)
	if err != nil {
		return Package{}, fmt.Errorf("read cached component %s: %w", component, err)
	}
	var selected Package
	for _, versionEntry := range versions {
		if !versionEntry.IsDir() {
			continue
		}
		versionRoot := filepath.Join(componentRoot, versionEntry.Name())
		digests, readErr := os.ReadDir(versionRoot)
		if readErr != nil {
			continue
		}
		for _, digestEntry := range digests {
			if !digestEntry.IsDir() {
				continue
			}
			root := filepath.Join(versionRoot, digestEntry.Name())
			manifest, validateErr := validateDirectory(root)
			if validateErr != nil ||
				manifest.Component.ID != component ||
				!manifest.supportsSoftwareVersion(softwareVersion) ||
				(checkCompatibility && !compatibleWithHost(manifest, r.host)) ||
				!packageSupportsActions(manifest, requiredActions) {
				continue
			}
			if selected.Root == "" ||
				compareVersions(manifest.Component.Version, selected.Manifest.Component.Version) > 0 {
				selected = Package{Manifest: manifest, Root: root, Source: "cache"}
			}
		}
	}
	if selected.Root == "" {
		return Package{}, fmt.Errorf("no compatible cached %s package for software version %s", component, softwareVersion)
	}
	return selected, nil
}

func (r *Registry) resolveBundledInstalled(
	component string,
	softwareVersion string,
	requiredActions []string,
	compatibilityRequired ...bool,
) (Package, error) {
	checkCompatibility := true
	if len(compatibilityRequired) > 0 {
		checkCompatibility = compatibilityRequired[0]
	}
	componentRoot := filepath.Join(r.config.BundledPath, component)
	entries, err := os.ReadDir(componentRoot)
	if err != nil {
		return Package{}, fmt.Errorf("read bundled component %s: %w", component, err)
	}
	var selected Package
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(componentRoot, entry.Name())
		manifest, validateErr := validateDirectory(root)
		if validateErr != nil ||
			manifest.Component.ID != component ||
			!manifest.supportsSoftwareVersion(softwareVersion) ||
			(checkCompatibility && !compatibleWithHost(manifest, r.host)) ||
			!packageSupportsActions(manifest, requiredActions) {
			continue
		}
		if selected.Root == "" ||
			compareVersions(manifest.Component.Version, selected.Manifest.Component.Version) > 0 {
			selected = Package{Manifest: manifest, Root: root, Source: "bundled"}
		}
	}
	if selected.Root == "" {
		return Package{}, fmt.Errorf("no compatible bundled %s package for installed software version %s", component, softwareVersion)
	}
	return selected, nil
}

func packageSupportsActions(manifest Manifest, requiredActions []string) bool {
	actions := manifest.actionMap()
	for _, action := range requiredActions {
		if strings.TrimSpace(actions[action]) == "" {
			return false
		}
	}
	return true
}

func (r *Registry) resolveRemote(ctx context.Context, component, softwareVersion string) (Package, error) {
	metadata, err := r.resolveRemoteMetadata(ctx, component, softwareVersion)
	if err != nil {
		return Package{}, err
	}
	return r.downloadAndPrepare(ctx, metadata)
}

func (r *Registry) resolveRemoteMetadata(ctx context.Context, component, softwareVersion string) (Metadata, error) {
	if err := r.checkReady(ctx); err != nil {
		return Metadata{}, err
	}
	requestBody, err := json.Marshal(ResolveRequest{
		Component:       component,
		SoftwareVersion: softwareVersion,
		Channel:         r.config.Channel,
		Host:            r.host,
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("encode resolve request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint("/v1/packages/resolve"), bytes.NewReader(requestBody))
	if err != nil {
		return Metadata{}, fmt.Errorf("create resolve request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return Metadata{}, fmt.Errorf("resolve package: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Metadata{}, decodeAPIError(response)
	}
	var metadata Metadata
	if err := decodeLimitedJSON(response.Body, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("decode package metadata: %w", err)
	}
	if metadata.Manifest.Component.ID != component ||
		!metadata.Manifest.supportsSoftwareVersion(softwareVersion) ||
		metadata.Manifest.Component.Channel != r.config.Channel {
		return Metadata{}, fmt.Errorf("script center returned a package that does not match the request")
	}
	if err := metadata.Manifest.validate(); err != nil {
		return Metadata{}, err
	}
	if err := r.verifyMetadata(metadata); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func (r *Registry) checkReady(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.endpoint("/health/ready"), nil)
	if err != nil {
		return err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("script center connectivity check failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("script center is not ready: HTTP %d", response.StatusCode)
	}
	return nil
}

func (r *Registry) verifyMetadata(metadata Metadata) error {
	if len(metadata.SHA256) != sha256.Size*2 {
		return fmt.Errorf("invalid package digest")
	}
	if _, err := hex.DecodeString(metadata.SHA256); err != nil {
		return fmt.Errorf("invalid package digest: %w", err)
	}
	if metadata.Size < 1 || metadata.Size > r.config.MaxPackageBytes {
		return fmt.Errorf("package size is outside the configured limit")
	}
	publicKeyEncoded, trusted := r.config.TrustedKeys[metadata.KeyID]
	if !trusted {
		return fmt.Errorf("package signing key %s is not trusted", metadata.KeyID)
	}
	publicKey, err := base64.StdEncoding.DecodeString(publicKeyEncoded)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("trusted key %s is invalid", metadata.KeyID)
	}
	signature, err := base64.StdEncoding.DecodeString(metadata.Signature)
	if err != nil {
		return fmt.Errorf("decode package signature: %w", err)
	}
	component := metadata.Manifest.Component
	payload := signingPayload(component.ID, component.Version, metadata.SHA256, metadata.Size)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return fmt.Errorf("package signature verification failed")
	}
	downloadURL, err := url.Parse(metadata.DownloadURL)
	if err != nil || downloadURL.Scheme != r.baseURL.Scheme || !strings.EqualFold(downloadURL.Host, r.baseURL.Host) {
		return fmt.Errorf("package download URL is outside the configured script center")
	}
	return nil
}

func (r *Registry) downloadAndPrepare(ctx context.Context, metadata Metadata) (Package, error) {
	component := metadata.Manifest.Component
	destination := filepath.Join(r.config.CachePath, "components", component.ID, component.Version, metadata.SHA256)
	if manifest, err := validateDirectory(destination); err == nil &&
		manifest.Component.ID == component.ID && manifest.Component.Version == component.Version {
		return Package{Manifest: manifest, Root: destination, Source: "cache"}, nil
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0750); err != nil {
		return Package{}, fmt.Errorf("create script cache directory: %w", err)
	}
	archive, err := os.CreateTemp(r.config.CachePath, "download-*.tar.gz")
	if err != nil {
		return Package{}, fmt.Errorf("create package download: %w", err)
	}
	archiveName := archive.Name()
	defer os.Remove(archiveName)
	if err := archive.Chmod(0640); err != nil {
		archive.Close()
		return Package{}, fmt.Errorf("secure package download: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata.DownloadURL, nil)
	if err != nil {
		archive.Close()
		return Package{}, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		archive.Close()
		return Package{}, fmt.Errorf("download package: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		archive.Close()
		return Package{}, fmt.Errorf("download package: HTTP %d", response.StatusCode)
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(archive, hash), io.LimitReader(response.Body, r.config.MaxPackageBytes+1))
	bodyCloseErr := response.Body.Close()
	archiveCloseErr := archive.Close()
	if copyErr != nil {
		return Package{}, fmt.Errorf("download package: %w", copyErr)
	}
	if bodyCloseErr != nil {
		return Package{}, fmt.Errorf("close package response: %w", bodyCloseErr)
	}
	if archiveCloseErr != nil {
		return Package{}, fmt.Errorf("close package download: %w", archiveCloseErr)
	}
	if size != metadata.Size || size > r.config.MaxPackageBytes {
		return Package{}, fmt.Errorf("downloaded package size does not match signed metadata")
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != strings.ToLower(metadata.SHA256) {
		return Package{}, fmt.Errorf("downloaded package digest does not match signed metadata")
	}
	manifest, err := extractPackage(archiveName, destination, r.config.MaxExpandedBytes)
	if err != nil {
		return Package{}, err
	}
	if manifest.Component.ID != component.ID || manifest.Component.Version != component.Version {
		_ = os.RemoveAll(destination)
		return Package{}, fmt.Errorf("package manifest does not match signed metadata")
	}
	return Package{Manifest: manifest, Root: destination, Source: "remote"}, nil
}

func (r *Registry) resolveBundled(component, softwareVersion string) (Package, error) {
	componentRoot := filepath.Join(r.config.BundledPath, component)
	entries, err := os.ReadDir(componentRoot)
	if err != nil {
		return Package{}, fmt.Errorf("read bundled component %s: %w", component, err)
	}
	var selected Package
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(componentRoot, entry.Name())
		manifest, validateErr := validateDirectory(root)
		if validateErr != nil ||
			manifest.Component.ID != component ||
			manifest.Component.Channel != r.config.Channel ||
			!manifest.supportsSoftwareVersion(softwareVersion) ||
			!compatibleWithHost(manifest, r.host) {
			continue
		}
		if selected.Root == "" || compareVersions(manifest.Component.Version, selected.Manifest.Component.Version) > 0 {
			selected = Package{Manifest: manifest, Root: root, Source: "bundled"}
		}
	}
	if selected.Root == "" {
		return Package{}, fmt.Errorf("no compatible bundled %s package for software version %s", component, softwareVersion)
	}
	return selected, nil
}

func (r *Registry) endpoint(apiPath string) string {
	return strings.TrimRight(r.baseURL.String(), "/") + apiPath
}

func decodeAPIError(response *http.Response) error {
	var envelope APIError
	if err := decodeLimitedJSON(response.Body, &envelope); err == nil && envelope.Error.Message != "" {
		return errors.New(envelope.Error.Message)
	}
	return fmt.Errorf("script center returned HTTP %d", response.StatusCode)
}

func decodeLimitedJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(destination)
}

func signingPayload(componentID, version, digest string, size int64) []byte {
	return []byte(fmt.Sprintf("oneinstack-script-package-v1\n%s\n%s\n%s\n%d\n", componentID, version, digest, size))
}

func detectHost() Host {
	systemID, systemVersion := readOSRelease("/etc/os-release")
	return Host{
		PanelVersion:  buildinfo.CompatibleVersion(),
		SystemID:      systemID,
		SystemVersion: systemVersion,
		Architecture:  runtime.GOARCH,
	}
}

func readOSRelease(fileName string) (string, string) {
	contents, err := os.ReadFile(fileName)
	if err != nil {
		return runtime.GOOS, "unknown"
	}
	values := make(map[string]string)
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found || strings.HasPrefix(strings.TrimSpace(key), "#") {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return strings.ToLower(values["ID"]), values["VERSION_ID"]
}

func compatibleWithHost(manifest Manifest, host Host) bool {
	architectureMatches := false
	for _, architecture := range manifest.Compatibility.Architectures {
		if architecture == host.Architecture {
			architectureMatches = true
			break
		}
	}
	if !architectureMatches {
		return false
	}
	for _, system := range manifest.Compatibility.Systems {
		if system.ID != host.SystemID {
			continue
		}
		for _, version := range system.Versions {
			if systemVersionMatches(version, host.SystemVersion) {
				return true
			}
		}
	}
	return false
}

func systemVersionMatches(supported, actual string) bool {
	supported = strings.TrimSpace(supported)
	actual = strings.TrimSpace(actual)
	return supported == "*" || supported == actual ||
		(!strings.Contains(supported, ".") && strings.HasPrefix(actual, supported+"."))
}

func compareVersions(left, right string) int {
	parse := func(value string) [3]int {
		value = strings.TrimPrefix(value, "v")
		if index := strings.IndexAny(value, "-+"); index >= 0 {
			value = value[:index]
		}
		var result [3]int
		for index, part := range strings.Split(value, ".") {
			if index >= len(result) {
				break
			}
			result[index], _ = strconv.Atoi(part)
		}
		return result
	}
	l, rr := parse(left), parse(right)
	for index := range l {
		if l[index] < rr[index] {
			return -1
		}
		if l[index] > rr[index] {
			return 1
		}
	}
	return strings.Compare(left, right)
}

// ComparePackageVersions compares component package versions using the same
// ordering as package resolution.
func ComparePackageVersions(left, right string) int {
	return compareVersions(left, right)
}
