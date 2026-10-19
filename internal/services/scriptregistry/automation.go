package scriptregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// ResolvePublishedAutomation pins an existing public component when only Panel
// code changed. It never substitutes a newer package or republishes a component.
func (r *Registry) ResolvePublishedAutomation(ctx context.Context, component, software, version, expectedSHA string) (Package, error) {
	if r.baseURL == nil || !componentIDPattern.MatchString(component) || !versionPattern.MatchString(version) {
		return Package{}, fmt.Errorf("invalid fixed public package identity")
	}
	body, _ := json.Marshal(ResolveRequest{Component: component, Version: version, SoftwareVersion: software, Host: r.host})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL.String()+"/v1/packages/resolve", bytes.NewReader(body))
	if err != nil {
		return Package{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return Package{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Package{}, decodeAPIError(response)
	}
	var metadata Metadata
	if err = decodeLimitedJSON(response.Body, &metadata); err != nil {
		return Package{}, err
	}
	if metadata.Manifest.Component.ID != component || metadata.Manifest.Component.Version != version || metadata.SHA256 != expectedSHA ||
		!metadata.Manifest.supportsSoftwareVersion(software) || !compatibleWithHost(metadata.Manifest, r.host) || !metadata.Manifest.sourceAvailableForHost(software, r.host) {
		return Package{}, fmt.Errorf("public package does not match the approved replay identity")
	}
	if err = metadata.Manifest.validate(); err != nil {
		return Package{}, err
	}
	if err = r.verifyMetadata(metadata); err != nil {
		return Package{}, err
	}
	download, err := url.Parse(metadata.DownloadURL)
	if err != nil || download.Scheme != r.baseURL.Scheme || download.Host != r.baseURL.Host || download.Path != "/v1/packages/"+component+"/"+version+"/download" || download.RawQuery != "" || download.Fragment != "" {
		return Package{}, fmt.Errorf("invalid fixed public package endpoint")
	}
	return r.downloadAndPrepare(ctx, metadata)
}

const AutomationSignatureDomain = "oneinstack-center-automation-v1"

// AutomationRequester signs with the existing Panel identity; private keys never leave Panel.
type AutomationRequester interface {
	NewRequest(context.Context, string, string, string, []byte) (*http.Request, error)
	Do(*http.Request) (*http.Response, error)
}

func (r *Registry) Host() Host { return r.host }

func (r *Registry) ResolveAutomation(ctx context.Context, job, component, software, version, expectedSHA, planDigest string, signer AutomationRequester) (Package, error) {
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(job) || !componentIDPattern.MatchString(component) || !versionPattern.MatchString(version) || signer == nil {
		return Package{}, fmt.Errorf("invalid isolated package identity")
	}
	body, err := json.Marshal(struct {
		ResolveRequest
		PlanDigest string `json:"planDigest"`
	}{ResolveRequest: ResolveRequest{Component: component, Version: version, SoftwareVersion: software, Host: r.host}, PlanDigest: planDigest})
	if err != nil {
		return Package{}, err
	}
	path := "/v1/panel/automation/" + job + "/resolve"
	request, err := signer.NewRequest(ctx, http.MethodPost, path, AutomationSignatureDomain, body)
	if err != nil {
		return Package{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := signer.Do(request)
	if err != nil {
		return Package{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Package{}, decodeAPIError(response)
	}
	var metadata Metadata
	if err := decodeLimitedJSON(response.Body, &metadata); err != nil {
		return Package{}, err
	}
	if metadata.Manifest.Component.ID != component || metadata.Manifest.Component.Version != version || metadata.SHA256 != expectedSHA ||
		!metadata.Manifest.supportsSoftwareVersion(software) || !compatibleWithHost(metadata.Manifest, r.host) ||
		!metadata.Manifest.sourceAvailableForHost(software, r.host) {
		return Package{}, fmt.Errorf("isolated metadata does not match the approved package or host")
	}
	if err := metadata.Manifest.validate(); err != nil {
		return Package{}, err
	}
	if err := r.verifyMetadata(metadata); err != nil {
		return Package{}, err
	}
	download, err := url.Parse(metadata.DownloadURL)
	if err != nil || r.baseURL == nil || download.Scheme != r.baseURL.Scheme || download.Host != r.baseURL.Host ||
		download.Path != "/v1/panel/automation/"+job+"/download" || download.RawQuery != "" {
		return Package{}, fmt.Errorf("invalid isolated download endpoint")
	}
	// Private drafts must never become candidates for ordinary cache fallback.
	isolated := *r
	isolated.config.CachePath = filepath.Join(r.config.CachePath, "automation", job)
	isolated.automationRequester = signer
	return isolated.downloadAndPrepare(ctx, metadata)
}

func (r *Registry) automationCacheRoot(packageURL string) (string, error) {
	parsed, err := url.Parse(packageURL)
	if err != nil {
		return "", fmt.Errorf("invalid fixed package URL")
	}
	if !strings.HasPrefix(parsed.Path, "/v1/panel/automation/") {
		return r.config.CachePath, nil
	}
	parts := strings.Split(parsed.Path, "/")
	if r.baseURL == nil || parsed.Scheme != r.baseURL.Scheme || parsed.Host != r.baseURL.Host || parsed.RawQuery != "" || parsed.Fragment != "" || len(parts) != 6 || parts[5] != "download" || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(parts[4]) {
		return "", fmt.Errorf("invalid fixed isolated package URL")
	}
	return filepath.Join(r.config.CachePath, "automation", parts[4]), nil
}

func (p Package) AutomationPin(software string) PackagePin {
	return PackagePin{Component: p.Manifest.Component.ID, SoftwareVersion: software,
		Channel: p.Manifest.Component.Channel, ResolvedVersion: p.Manifest.Component.Version,
		PackageSource: p.Source, PackageURL: p.Metadata.DownloadURL, PackageSHA256: p.Metadata.SHA256,
		Signature: p.Metadata.Signature, KeyID: p.Metadata.KeyID,
		PublisherFingerprint: p.Metadata.PublisherFingerprint,
		TargetOS:             p.Metadata.TargetOS, TargetOSVersion: p.Metadata.TargetOSVersion,
		TargetArch: p.Metadata.TargetArch, ReleaseRevision: p.Metadata.ReleaseRevision}
}
