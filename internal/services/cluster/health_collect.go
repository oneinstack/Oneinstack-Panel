package cluster

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/internal/services/monitoring"
	"oneinstack/internal/services/panelbackup"
	"oneinstack/internal/services/storage"
)

const healthBackupMaxAge = 7 * 24 * time.Hour

// CollectLocalHealth is read-only. It reports managed resources and safe
// reason codes, without sending paths, credentials, or raw probe errors.
func CollectLocalHealth(ctx context.Context) ([]HealthObservation, error) {
	db := app.DB()
	if db == nil {
		return nil, fmt.Errorf("cluster health database is unavailable")
	}
	now := time.Now().UTC()
	items := make([]HealthObservation, 0, 64)
	add := func(kind, id, check, name, target, status, reason string) {
		name = strings.TrimSpace(strings.Map(func(char rune) rune {
			if char == '\n' || char == '\r' || char == 0 {
				return ' '
			}
			return char
		}, name))
		if strings.TrimSpace(name) == "" {
			name = kind + " #" + id
		}
		if len(name) > 160 {
			var short strings.Builder
			for _, char := range name {
				if short.Len()+len(string(char)) > 160 {
					break
				}
				short.WriteRune(char)
			}
			name = short.String()
		}
		items = append(items, HealthObservation{
			ResourceType: kind, ResourceID: id, Check: check, Name: name,
			Target: target, Status: status, Reason: reason, ObservedAt: now,
		})
	}

	var sites []models.Website
	if err := db.WithContext(ctx).Order("id ASC").Find(&sites).Error; err != nil {
		return nil, err
	}
	var certificates []models.Certificate
	if err := db.WithContext(ctx).Find(&certificates).Error; err != nil {
		return nil, err
	}
	certBySite := make(map[int64]models.Certificate, len(certificates))
	for _, cert := range certificates {
		certBySite[cert.WebsiteID] = cert
	}
	siteResults := make([]HealthObservation, len(sites))
	sem := make(chan struct{}, 8)
	var siteWait sync.WaitGroup
	for i, site := range sites {
		siteWait.Add(1)
		sem <- struct{}{}
		go func(i int, site models.Website) {
			defer siteWait.Done()
			defer func() { <-sem }()
			result := HealthObservation{ResourceType: "website", ResourceID: strconv.FormatInt(site.ID, 10), Check: "http", Name: site.Name}
			if !site.Enabled {
				result.Status, result.Reason = healthDisabled, "site_disabled"
			} else {
				cert, hasCert := certBySite[site.ID]
				result.Target = managedSiteTarget(site.Domain, hasCert && cert.Status != models.CertificateStatusDisabled)
				if result.Target == "" {
					result.Status, result.Reason = healthUnknown, "invalid_site_domain"
				} else {
					result.Status, result.Reason = probeLocalWebsite(ctx, result.Target)
				}
			}
			siteResults[i] = result
		}(i, site)
	}
	siteWait.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, result := range siteResults {
		add(result.ResourceType, result.ResourceID, result.Check, result.Name, result.Target, result.Status, result.Reason)
	}

	var managed []models.ManagedCertificate
	if err := db.WithContext(ctx).Find(&managed).Error; err != nil {
		return nil, err
	}
	managedIDs := make(map[string]struct{}, len(managed))
	for _, cert := range managed {
		managedIDs[cert.ID] = struct{}{}
	}
	for _, cert := range certificates {
		if _, exists := managedIDs[cert.ManagedID]; exists {
			continue
		}
		status, reason := certificateHealth(cert.Status, cert.CertificatePath, now)
		add("certificate", cert.ID, "expiry", cert.Domains, "", status, reason)
	}
	for _, cert := range managed {
		status, reason := certificateHealth(cert.Status, cert.CertificatePath, now)
		add("certificate", cert.ID, "managed_expiry", cert.Domains, "", status, reason)
	}

	if manager := monitoring.Default(); manager != nil {
		services, err := manager.ListServiceHealth(ctx, false)
		if err != nil {
			return nil, err
		}
		for _, service := range services {
			status, reason := healthHealthy, "service_active"
			if now.Sub(service.LastCheckedAt) > 10*time.Minute {
				status, reason = healthUnknown, "service_check_stale"
			} else if service.HealthState == models.MonitorStateFiring || service.ServiceState == "failed" {
				status, reason = healthCritical, "service_failed"
			} else if service.HealthState == models.MonitorStatePending || service.Busy {
				status, reason = healthWarning, "service_transitioning"
			}
			add("service", service.Component, "runtime", service.DisplayName, "", status, reason)
		}
	}

	var connections []models.Storage
	if err := db.WithContext(ctx).Where("type IN ?", []string{"mysql", "redis"}).Find(&connections).Error; err != nil {
		return nil, err
	}
	localConnection := make(map[int64]bool, len(connections))
	for _, connection := range connections {
		address := strings.ToLower(strings.TrimSpace(connection.Addr))
		if address != "127.0.0.1" && address != "localhost" && address != "::1" {
			continue
		}
		if !strings.Contains(connection.Remark, "面板自动管理") {
			continue
		}
		localConnection[connection.ID] = true
		probeCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		err := storage.ProbeManagedLocalConnection(probeCtx, connection.ID)
		cancel()
		status, reason := healthHealthy, "database_reachable"
		if err != nil {
			status, reason = healthCritical, "database_unreachable"
		}
		add("database", strconv.FormatInt(connection.ID, 10), "connection", strings.ToUpper(connection.Type), "", status, reason)
	}

	backups, listErr := panelbackup.ListApplicationBackupsReadOnly()
	status, reason := healthUnprotected, "no_backup"
	if listErr != nil {
		status, reason = healthUnknown, "backup_list_failed"
	} else if len(backups) > 0 {
		status, reason = backupAgeStatus(now, backups[0].CreatedAt)
	}
	add("backup", "panel", "freshness", "Panel", "", status, reason)
	var siteBackups []models.WebsiteBackup
	if err := db.WithContext(ctx).Order("created_at DESC").Find(&siteBackups).Error; err != nil {
		return nil, err
	}
	latestSiteBackup := make(map[int64]models.WebsiteBackup)
	for _, backup := range siteBackups {
		if _, ok := latestSiteBackup[backup.WebsiteID]; !ok {
			latestSiteBackup[backup.WebsiteID] = backup
		}
	}
	for _, site := range sites {
		status, reason := healthUnprotected, "no_backup"
		if backup, ok := latestSiteBackup[site.ID]; ok {
			status, reason = backupArtifactStatus(now, backup.CreatedAt, backup.FilePath, backup.SizeBytes)
		}
		add("backup", "website:"+strconv.FormatInt(site.ID, 10), "freshness", site.Name, "", status, reason)
	}
	var libraries []models.Library
	if err := db.WithContext(ctx).Where("type = ?", "mysql").Find(&libraries).Error; err != nil {
		return nil, err
	}
	var dbBackups []models.DatabaseBackup
	if err := db.WithContext(ctx).Order("created_at DESC").Find(&dbBackups).Error; err != nil {
		return nil, err
	}
	latestDBBackup := make(map[int64]models.DatabaseBackup)
	for _, backup := range dbBackups {
		if _, ok := latestDBBackup[backup.LibraryID]; !ok {
			latestDBBackup[backup.LibraryID] = backup
		}
	}
	for _, library := range libraries {
		if !localConnection[library.PID] {
			continue
		}
		status, reason := healthUnprotected, "no_backup"
		if backup, ok := latestDBBackup[library.ID]; ok {
			status, reason = backupArtifactStatus(now, backup.CreatedAt, backup.FilePath, backup.SizeBytes)
		}
		add("backup", "database:"+strconv.FormatInt(library.ID, 10), "freshness", library.Name, "", status, reason)
	}
	if len(items) > 500 {
		return nil, fmt.Errorf("cluster health resource limit exceeded")
	}
	return items, nil
}

func managedSiteTarget(domains string, https bool) string {
	host := strings.TrimSpace(strings.Split(domains, ",")[0])
	if host == "" || len(host) > 240 || strings.ContainsAny(host, " /\\?#@*") || strings.Contains(host, ":") {
		return ""
	}
	scheme := "http://"
	if https {
		scheme = "https://"
	}
	return scheme + host + "/"
}

func probeLocalWebsite(ctx context.Context, target string) (string, string) {
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return healthUnknown, "invalid_site_domain"
	}
	request.Header.Set("User-Agent", "OneinStack-Panel/cluster-health")
	response, err := client.Do(request)
	if err != nil {
		return healthCritical, "origin_unreachable"
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 400:
		return healthHealthy, "origin_reachable"
	case response.StatusCode == 401 || response.StatusCode == 403:
		return healthWarning, "origin_auth_required"
	default:
		return healthCritical, "origin_http_error"
	}
}

func certificateHealth(state, path string, now time.Time) (string, string) {
	if state == models.CertificateStatusDisabled {
		return healthDisabled, "certificate_disabled"
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return healthCritical, "certificate_missing"
	}
	if info.Size() == 0 || info.Size() > 1<<20 {
		return healthCritical, "certificate_invalid"
	}
	file, err := os.Open(path)
	if err != nil {
		return healthCritical, "certificate_unreadable"
	}
	content, err := io.ReadAll(io.LimitReader(file, 1<<20))
	_ = file.Close()
	if err != nil {
		return healthCritical, "certificate_unreadable"
	}
	block, _ := pem.Decode(content)
	if block != nil {
		content = block.Bytes
	}
	certificate, err := x509.ParseCertificate(content)
	if err != nil {
		return healthCritical, "certificate_invalid"
	}
	if now.Before(certificate.NotBefore) {
		return healthCritical, "certificate_not_yet_valid"
	}
	if !certificate.NotAfter.After(now) {
		return healthCritical, "certificate_expired"
	}
	if certificate.NotAfter.Before(now.Add(30 * 24 * time.Hour)) {
		return healthWarning, "certificate_expiring"
	}
	return healthHealthy, "certificate_valid"
}

func backupAgeStatus(now, created time.Time) (string, string) {
	if now.Sub(created) > healthBackupMaxAge {
		return healthWarning, "backup_stale"
	}
	return healthHealthy, "backup_recent"
}

func backupArtifactStatus(now, created time.Time, path string, size int64) (string, string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return healthCritical, "backup_artifact_missing"
	}
	return backupAgeStatus(now, created)
}
