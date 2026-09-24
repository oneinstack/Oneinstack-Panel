package cluster

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"time"

	"oneinstack/internal/models"
)

var blockedWebsiteProbePrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func allowedWebsiteProbeAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !allowedEndpointProbeAddress(address) || address.IsPrivate() {
		return false
	}
	for _, prefix := range blockedWebsiteProbePrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func newWebsiteProbeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(addresses) == 0 {
			return nil, errEndpointProbeNoAddress
		}
		for _, candidate := range addresses {
			if !allowedWebsiteProbeAddress(candidate) {
				return nil, errEndpointProbeUnsafeAddress
			}
		}
		var lastErr error
		for _, candidate := range addresses {
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}, TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 4 * time.Second, MaxIdleConns: 4, IdleConnTimeout: 15 * time.Second}
	return &http.Client{Transport: transport, Timeout: 6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func probeWebsiteEntry(ctx context.Context, client *http.Client, target string) (string, string) {
	parsed, err := url.Parse(target)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Hostname() == "" || parsed.Port() != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/" {
		return healthUnknown, "entry_target_invalid"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return healthUnknown, "entry_target_invalid"
	}
	request.Header.Set("User-Agent", "OneinStack-Panel/cluster-health")
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(err, errEndpointProbeUnsafeAddress) || errors.Is(err, errEndpointProbeNoAddress) {
			return healthUnknown, "entry_unprobeable"
		}
		return healthCritical, "entry_unreachable"
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 400:
		return healthHealthy, "entry_reachable"
	case response.StatusCode == 401 || response.StatusCode == 403:
		return healthWarning, "entry_auth_required"
	default:
		return healthCritical, "entry_http_error"
	}
}

func (m *Manager) ProbeWebsiteEntries(ctx context.Context) error {
	var rows []models.ClusterHealthResource
	if err := m.db.Where("present = ? AND resource_type = ? AND `check` = ? AND local_status <> ? AND target <> ''", true, "website", "http", healthDisabled).
		Order("node_id ASC, id ASC").Limit(500).Find(&rows).Error; err != nil {
		return err
	}
	var nodes []models.ClusterNode
	if err := m.db.Select("id", "status").Find(&nodes).Error; err != nil {
		return err
	}
	offline := make(map[uint]bool, len(nodes))
	for _, node := range nodes {
		offline[node.ID] = node.Status != models.ClusterNodeStatusOnline
	}
	client := newWebsiteProbeClient()
	defer client.CloseIdleConnections()
	type result struct {
		row            models.ClusterHealthResource
		status, reason string
	}
	results := make(chan result, len(rows))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		if offline[row.NodeID] {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(row models.ClusterHealthResource) {
			defer wg.Done()
			defer func() { <-sem }()
			status, reason := probeWebsiteEntry(ctx, client, row.Target)
			results <- result{row: row, status: status, reason: reason}
		}(row)
	}
	wg.Wait()
	close(results)
	for item := range results {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		observation := HealthObservation{ResourceType: "website", ResourceID: item.row.ResourceID,
			Check: "http", Name: item.row.Name, Target: item.row.Target,
			Status: item.status, Reason: item.reason, ObservedAt: time.Now().UTC()}
		if err := m.applyHealthObservation(ctx, item.row.NodeID, observation, true); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) MarkStaleHealthUnknown(now time.Time) error {
	// A missing report is not proof that the workload recovered or failed.
	return m.db.Model(&models.ClusterHealthResource{}).
		Where("present = ? AND resource_type NOT IN ? AND observed_at IS NOT NULL AND observed_at < ?", true, []string{"node", "task"}, now.Add(-12*time.Minute)).
		Updates(map[string]any{"local_status": healthUnknown, "local_reason": "report_stale", "status": healthUnknown, "reason": "report_stale"}).Error
}
