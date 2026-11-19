package website

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WebsiteHTTPProbe reports only the response observed from this Panel host.
type WebsiteHTTPProbe struct {
	Status    string            `json:"status"`
	CheckedAt time.Time         `json:"checkedAt"`
	Listeners []WebsiteListener `json:"listeners,omitempty"`
}

type WebsiteListener struct {
	Protocol string `json:"protocol"`
	Port     int    `json:"port"`
	Status   string `json:"status"`
	HTTPCode int    `json:"httpCode,omitempty"`
	bindHost string
}

var (
	websiteNginxListen  = regexp.MustCompile(`(?m)^[ \t]*listen[ \t]+([^;#\r\n]+);`)
	websiteApacheListen = regexp.MustCompile(`(?im)^[ \t]*<VirtualHost[ \t]+([^>\r\n]+)>`)
)

func ProbeHTTP(ctx context.Context, id int64) (WebsiteHTTPProbe, error) {
	service, err := resolveWebsiteService(false)
	if err != nil {
		return WebsiteHTTPProbe{Status: "config_unavailable", CheckedAt: time.Now().UTC()}, nil
	}
	site, err := service.Get(id)
	if err != nil {
		return WebsiteHTTPProbe{}, err
	}
	result := WebsiteHTTPProbe{CheckedAt: time.Now().UTC(), Status: "disabled"}
	if !site.Enabled {
		return result, nil
	}
	domains, _, err := normalizeDomains(site.Domain, site.Name)
	host := ""
	for _, name := range domains {
		if !strings.HasPrefix(name, "*.") {
			host = name
			break
		}
	}
	if err != nil || host == "" {
		result.Status = "unsupported"
		return result, nil
	}
	manager, err := service.managedConfigManagerForSite(site)
	if err != nil {
		result.Status = "config_unavailable"
		return result, nil
	}
	relative, err := service.managedConfigRelativePath(manager, site)
	if err != nil {
		result.Status = "config_unavailable"
		return result, nil
	}
	// Read the current file without the automatic repair path used by the
	// configuration editor. A health check must never publish a missing vhost.
	document, err := manager.Read(relative)
	if err != nil {
		result.Status = "config_unavailable"
		return result, nil
	}
	result.Listeners = websiteListeners(manager.Server.Component, document.Content)
	if len(result.Listeners) == 0 {
		result.Status = "unsupported"
		return result, nil
	}
	result.Status = "checked"
	for i := range result.Listeners {
		listener := &result.Listeners[i]
		if listener.Status == "" {
			if !manager.Server.Running {
				listener.Status = "service_stopped"
				continue
			}
			listener.Status, listener.HTTPCode = checkWebsiteListener(ctx, host, *listener)
		}
	}
	return result, nil
}

func websiteListeners(engine, content string) []WebsiteListener {
	var pattern *regexp.Regexp
	switch engine {
	case "nginx", "tengine", "openresty":
		pattern = websiteNginxListen
	case "apache":
		pattern = websiteApacheListen
	default:
		return nil
	}
	result := make([]WebsiteListener, 0, 2)
	seen := make(map[WebsiteListener]bool)
	for _, match := range pattern.FindAllStringSubmatch(content, -1) {
		fields := strings.Fields(match[1])
		if len(fields) == 0 || strings.HasPrefix(fields[0], "unix:") {
			continue
		}
		addr, port, ok := strings.Cut(fields[0], ":")
		if strings.HasPrefix(fields[0], "[") {
			addr, port, _ = net.SplitHostPort(fields[0])
			ok = port != ""
		}
		if !ok {
			port, addr = fields[0], ""
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			continue
		}
		listener := WebsiteListener{Protocol: "http", Port: number}
		if engine == "apache" && number == 443 {
			listener.Protocol = "https"
		}
		for _, option := range fields[1:] {
			if option == "ssl" {
				listener.Protocol = "https"
			}
			if option == "quic" || option == "udp" || option == "proxy_protocol" {
				listener.Status = "unsupported"
			}
		}
		parsedIP := net.ParseIP(addr)
		if addr != "" && addr != "*" && (parsedIP == nil || (!parsedIP.IsLoopback() && !parsedIP.IsUnspecified())) {
			listener.Status = "unsupported_address"
		}
		if parsedIP != nil && parsedIP.IsLoopback() {
			listener.bindHost = parsedIP.String()
		} else if addr == "::" {
			listener.bindHost = "::1"
		}
		if !seen[listener] {
			seen[listener] = true
			result = append(result, listener)
		}
		if len(result) == 4 {
			break
		}
	}
	return result
}

func checkWebsiteListener(ctx context.Context, host string, listener WebsiteListener) (string, int) {
	port := strconv.Itoa(listener.Port)
	transport := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: 450 * time.Millisecond}
			if listener.bindHost != "" {
				return dialer.DialContext(ctx, "tcp", net.JoinHostPort(listener.bindHost, port))
			}
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", port))
			if err != nil {
				return dialer.DialContext(ctx, "tcp", net.JoinHostPort("::1", port))
			}
			return conn, nil
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	target := url.URL{Scheme: listener.Protocol, Host: net.JoinHostPort(host, port), Path: "/"}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String(), nil)
	if err != nil {
		return "unsupported", 0
	}
	response, err := client.Do(request)
	if err != nil {
		var operation *net.OpError
		if errors.As(err, &operation) && operation.Op == "dial" {
			return "no_connection", 0
		}
		if listener.Protocol == "https" {
			var verification *tls.CertificateVerificationError
			if errors.As(err, &verification) {
				return "certificate_verification_failed", 0
			}
			return "https_failed", 0
		}
		return "no_response", 0
	}
	response.Body.Close()
	return "responded", response.StatusCode
}
