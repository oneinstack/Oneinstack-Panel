package software

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/scriptregistry"
	"oneinstack/router/input"
	"oneinstack/router/output"
)

// HostInstallationError contains only validated versions and stable codes.
// Package-manager stderr, repository URLs and credentials never enter the API.
type HostInstallationError struct {
	Code  string
	Info  output.HostInstallation
	cause error
}

func (e *HostInstallationError) Error() string     { return e.Code }
func (e *HostInstallationError) ErrorCode() string { return e.Code }

func (e *HostInstallationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func wrapHostInstallationError(info output.HostInstallation, fallback string, cause error) error {
	code := strings.TrimSpace(fallback)
	var coded interface{ ErrorCode() string }
	if errors.As(cause, &coded) {
		if candidate := strings.TrimSpace(coded.ErrorCode()); candidate != "" {
			code = candidate
		}
	}
	if code == "" {
		code = "HOST_VERSION_PROBE_FAILED"
	}
	return &HostInstallationError{Code: code, Info: info, cause: cause}
}

var firewalldHostCache struct {
	sync.Mutex
	key     string
	expires time.Time
	info    output.HostInstallation
}

const firewalldHostProbeTimeout = 30 * time.Second

func firewalldProbeIdentity() (string, string, string, error) {
	if app.DB() == nil {
		return "", "", "", errors.New("CATALOG_STALE")
	}
	var row models.Software
	if err := app.DB().Where("`key` = ? AND catalog_visible = ? AND catalog_managed = ?", "firewalld", true, true).
		Order("recommended DESC, version_order ASC, id DESC").First(&row).Error; err != nil {
		return "", "", "", errors.New("PACKAGE_UNPUBLISHED")
	}
	// This version only locates the signed component; it is never installed.
	// Do not require the persisted installable flag here: package availability
	// may have recovered after a previous catalog refresh marked the row stale.
	return row.Version, row.CatalogChannel, row.LatestPackageVersion, nil
}

func probeFirewalldInstallation(ctx context.Context, pin *scriptregistry.PackagePin, cached bool) (output.HostInstallation, error) {
	info := output.HostInstallation{Source: "host_repository", AvailableVersions: []string{}}
	version, channel, revision, err := firewalldProbeIdentity()
	if err != nil && pin == nil {
		return info, wrapHostInstallationError(info, "HOST_VERSION_PROBE_UNAVAILABLE", err)
	}
	cacheKey := app.ONE_CONFIG.ScriptCenter.URL + "|" + channel + "|" + version + "|" + revision
	if cached {
		firewalldHostCache.Lock()
		if firewalldHostCache.key == cacheKey && time.Now().Before(firewalldHostCache.expires) {
			info = firewalldHostCache.info
			info.AvailableVersions = slices.Clone(info.AvailableVersions)
			firewalldHostCache.Unlock()
			return info, nil
		}
		firewalldHostCache.Unlock()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Resolution may include a Center readiness check, metadata resolution, a
	// package download and a package-manager cache query. The old 8-second cap
	// made a slow but valid host look unsupported.
	ctx, cancel := context.WithTimeout(ctx, firewalldHostProbeTimeout)
	defer cancel()
	registry, err := scriptregistry.New(app.ONE_CONFIG.ScriptCenter)
	if err != nil {
		return info, wrapHostInstallationError(info, "HOST_VERSION_PROBE_UNAVAILABLE", err)
	}
	var pkg scriptregistry.Package
	if pin != nil {
		pkg, err = registry.ResolveFixed("firewalld", pin.SoftwareVersion, *pin)
	} else {
		pkg, err = registry.ResolveChannel(ctx, "firewalld", version, channel)
	}
	if err != nil {
		return info, wrapHostInstallationError(info, "HOST_VERSION_PROBE_UNAVAILABLE", err)
	}
	// Old component packages must not silently produce a global recommendation.
	declared := false
	for _, parameter := range pkg.Manifest.Parameters {
		if parameter.Name == "status-scope" && parameter.Env == "ONEINSTACK_STATUS_SCOPE" {
			declared = true
		}
	}
	if !declared {
		return info, wrapHostInstallationError(info, "PACKAGE_UNAVAILABLE", errors.New("component status probe parameter is missing"))
	}
	scriptInfo, err := scriptInfoFromPackage(pkg, "status")
	if err != nil {
		return info, wrapHostInstallationError(info, "PACKAGE_UNAVAILABLE", err)
	}
	// The installation probe only reads host identity, repository candidates
	// and firewall conflicts. It must not validate the optional Panel port;
	// the component manifest uses 0 to mean "not supplied", while the generic
	// port validator quite correctly accepts only real TCP ports.
	delete(scriptInfo.Params, "PANEL_PORT")
	scriptInfo.Params["ONEINSTACK_STATUS_SCOPE"] = "installation"
	data, err := NewInstaller().scriptManager.ExecuteProbe(ctx, scriptInfo, maxServiceProbeBytes)
	if err != nil {
		return info, wrapHostInstallationError(info, "HOST_VERSION_PROBE_FAILED", err)
	}
	invalidProbe := func() error {
		return wrapHostInstallationError(info, "HOST_VERSION_PROBE_FAILED", errors.New("component status probe returned invalid data"))
	}
	component, probe := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return info, invalidProbe()
		}
		switch key {
		case "component":
			component = value
		case "probe":
			probe = value
		case "system_id", "system_version", "package_manager":
			if len(value) > 64 || strings.ContainsAny(value, "\r\n\t /\\") {
				return info, invalidProbe()
			}
			switch key {
			case "system_id":
				info.SystemID = value
			case "system_version":
				info.SystemVersion = value
			case "package_manager":
				info.PackageManager = value
			}
		case "available_version", "installed_version":
			if value == "" {
				continue
			}
			if !softwareVersionPattern.MatchString(value) {
				return info, invalidProbe()
			}
			if key == "installed_version" {
				info.InstalledVersion = value
				continue
			}
			if scriptregistry.SupportsSoftwareVersion(pkg.Manifest.Component.SoftwareVersions, value) && !slices.Contains(info.AvailableVersions, value) {
				info.AvailableVersions = append(info.AvailableVersions, value)
			}
		case "conflicting_backend":
			if value != "" && value != "ufw" && value != "iptables" && value != "nftables" {
				return info, invalidProbe()
			}
			info.ConflictingBackend = value
		case "blocked_code":
			if value != "FIREWALL_BACKEND_CONFLICT" && value != "EXTERNAL_SERVICE_CONFLICT" {
				return info, invalidProbe()
			}
			info.BlockedCode = value
		case "repository_error":
			if value != "HOST_REPOSITORY_UNAVAILABLE" {
				return info, invalidProbe()
			}
			info.RepositoryError = value
		default:
			return info, invalidProbe()
		}
	}
	if component != "firewalld" || probe != "installation" || info.PackageManager == "" {
		return info, invalidProbe()
	}
	// A host package that is already installed can be adopted by the Panel
	// even when the repository no longer exposes that exact package candidate.
	// Keep this fallback restricted to a runtime version supported by the
	// signed component package; an arbitrary installed firewalld must not make
	// an unsupported host version appear installable.
	if len(info.AvailableVersions) == 0 && info.InstalledVersion != "" &&
		scriptregistry.SupportsSoftwareVersion(pkg.Manifest.Component.SoftwareVersions, info.InstalledVersion) {
		info.AvailableVersions = append(info.AvailableVersions, info.InstalledVersion)
		info.RepositoryError = ""
	}
	slices.SortFunc(info.AvailableVersions, func(a, b string) int { return scriptregistry.ComparePackageVersions(b, a) })
	if len(info.AvailableVersions) > 0 {
		info.RecommendedVersion = info.AvailableVersions[0]
	}
	if cached {
		firewalldHostCache.Lock()
		firewalldHostCache.key, firewalldHostCache.expires, firewalldHostCache.info = cacheKey, time.Now().Add(30*time.Second), info
		firewalldHostCache.Unlock()
	}
	return info, nil
}

func hydrateFirewalldInstallation(item *output.Software) {
	if !strings.EqualFold(item.Key, "firewalld") {
		return
	}
	info, err := probeFirewalldInstallation(context.Background(), nil, true)
	if err != nil {
		info.RepositoryError = "HOST_VERSION_PROBE_UNAVAILABLE"
		if err.Error() == "CATALOG_STALE" || err.Error() == "PACKAGE_UNPUBLISHED" {
			info.RepositoryError = err.Error()
		}
		var coded interface{ ErrorCode() string }
		if errors.As(err, &coded) {
			info.RepositoryError = coded.ErrorCode()
		}
	}
	availableVersions := firewalldExactVersionsByMajor(info.AvailableVersions)
	options := make([]output.VersionOption, 0, len(availableVersions))
	versions := make([]string, 0, len(availableVersions))
	for _, version := range availableVersions {
		var selected output.VersionOption
		var fallback output.VersionOption
		matched, hasFallback := false, false
		for _, option := range item.VersionOptions {
			if !option.Enabled {
				continue
			}
			if !hasFallback || option.Recommended {
				fallback, hasFallback = option, true
			}
			if option.Version != version && !(option.AllowCustomVersion && scriptregistry.SupportsSoftwareVersion([]string{option.Line}, version)) {
				continue
			}
			selected, matched = option, true
			break
		}
		if !matched {
			if !hasFallback {
				continue
			}
			// The signed component probe already proved that this exact installed
			// runtime is supported. Older catalogs used a synthetic firewalld
			// profile version instead of a matching version line, so reuse its
			// enabled presentation metadata instead of hiding an adoptable host
			// package behind installable=false.
			selected = fallback
		}
		selected.Version, selected.Recommended = version, len(versions) == 0
		// The list exposes only the exact host candidate. Version lines and
		// custom-version flags remain an internal catalog resolution detail.
		selected.Line = ""
		selected.AllowCustomVersion = false
		selected.Installable = firewalldInstallationAllowed(info)
		options, versions = append(options, selected), append(versions, version)
	}
	info.AvailableVersions = slices.Clone(versions)
	info.RecommendedVersion = ""
	if len(versions) > 0 {
		info.RecommendedVersion = versions[0]
	}
	if len(versions) == 0 && info.RepositoryError == "" {
		info.RepositoryError = "HOST_PACKAGE_VERSION_UNAVAILABLE"
	}
	item.RecommendedVersion = info.RecommendedVersion
	item.VersionOptions, item.VersionLines, item.Versions = options, []string{}, versions
	// The probe has already resolved and verified the signed Center package.
	// Recompute firewalld availability from that proof instead of retaining a
	// stale catalog flag that would otherwise make the disabled state sticky.
	item.Installable = len(versions) > 0 && firewalldInstallationAllowed(info)
	item.HostInstallation = &info
	for _, parameter := range item.Params {
		if parameter != nil && strings.EqualFold(parameter.Key, "software-version") {
			parameter.Default = info.RecommendedVersion
			parameter.Rule = ""
		}
	}
	currentVersion := info.InstalledVersion
	if currentVersion == "" {
		currentVersion = item.InstallVersion
	}
	softwareUpdate := item.Installed && info.RecommendedVersion != "" && scriptregistry.ComparePackageVersions(info.RecommendedVersion, currentVersion) > 0
	packageUpdate := item.Installed && item.InstalledPackageVersion != "" && scriptregistry.ComparePackageVersions(item.LatestPackageVersion, item.InstalledPackageVersion) > 0
	item.IsUpdate, item.UpdateReason = softwareUpdate || packageUpdate, ""
	switch {
	case softwareUpdate && packageUpdate:
		item.UpdateReason = "both"
	case softwareUpdate:
		item.UpdateReason = "software_version"
	case packageUpdate:
		item.UpdateReason = "component_package"
	}
}

// firewalldExactVersionsByMajor keeps the newest host-repository candidate
// from each major line. The catalog still retains version-line rows internally
// so exact requests can be authorized and resolved without exposing lines to
// the software list consumer.
func firewalldExactVersionsByMajor(available []string) []string {
	result := make([]string, 0, len(available))
	seenMajor := make(map[string]struct{}, len(available))
	for _, version := range available {
		version = strings.TrimSpace(version)
		if version == "" {
			continue
		}
		major := version
		if dot := strings.IndexByte(version, '.'); dot > 0 {
			major = version[:dot]
		}
		if _, exists := seenMajor[major]; exists {
			continue
		}
		seenMajor[major] = struct{}{}
		result = append(result, version)
	}
	return result
}

func firewalldInstallationAllowed(info output.HostInstallation) bool {
	if info.RepositoryError != "" {
		return false
	}
	if info.BlockedCode == "" {
		return true
	}
	if info.BlockedCode != "FIREWALL_BACKEND_CONFLICT" {
		return false
	}
	backend := strings.TrimSpace(info.ConflictingBackend)
	// An iptables host may install the firewalld package without taking over
	// its active rules; the component keeps firewalld stopped until an
	// explicitly guarded migration/start operation is requested.
	return strings.EqualFold(backend, "ufw") || strings.EqualFold(backend, "nftables") || strings.EqualFold(backend, "iptables")
}

// Resolve defaults and version lines before package pinning; explicit exact
// requests are never replaced by another version. The same signed status
// action is used by the software list, preview and direct installer callers.
func resolveFirewalldInstallParams(ctx context.Context, params *input.InstallParams) error {
	if params == nil || !strings.EqualFold(params.Key, "firewalld") {
		return nil
	}
	info, err := probeFirewalldInstallation(ctx, params.ResolvedPackage, false)
	if err != nil {
		if err.Error() == "HOST_VERSION_PROBE_UNAVAILABLE" {
			return &HostInstallationError{Code: "HOST_VERSION_PROBE_UNAVAILABLE", Info: info}
		}
		return err
	}
	if info.BlockedCode != "" && !firewalldInstallationAllowed(info) {
		return &HostInstallationError{Code: info.BlockedCode, Info: info}
	}
	if info.RepositoryError != "" {
		return &HostInstallationError{Code: info.RepositoryError, Info: info}
	}
	requested := strings.TrimSpace(params.Version)
	resolved := requested
	if requested == "" {
		resolved = info.RecommendedVersion
	}
	if strings.HasSuffix(requested, ".x") {
		resolved = ""
		for _, version := range info.AvailableVersions {
			if scriptregistry.SupportsSoftwareVersion([]string{requested}, version) {
				resolved = version
				break
			}
		}
	}
	if resolved == "" || !slices.Contains(info.AvailableVersions, resolved) {
		return &HostInstallationError{Code: "HOST_PACKAGE_VERSION_UNAVAILABLE", Info: info}
	}
	if params.ResolvedPackage != nil && params.ResolvedPackage.SoftwareVersion != resolved {
		return fmt.Errorf("VERSION_MISMATCH: resolved version differs from the preview package pin")
	}
	params.Version = resolved
	for key := range params.Parameters {
		if canonicalInstallParameterName(key) == "software-version" || key == "version" {
			params.Parameters[key] = resolved
		}
	}
	return nil
}
