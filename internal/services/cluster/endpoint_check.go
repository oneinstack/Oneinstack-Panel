package cluster

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

const (
	EndpointCheckHealthy = "healthy"
	EndpointCheckWarning = "warning"
	EndpointCheckInvalid = "invalid"

	EndpointCheckOK                = "ENDPOINT_OK"
	EndpointCheckInvalidURL        = "ENDPOINT_INVALID_URL"
	EndpointCheckUnsafeAddress     = "ENDPOINT_UNSAFE_ADDRESS"
	EndpointCheckDNSFailed         = "ENDPOINT_DNS_FAILED"
	EndpointCheckTimeout           = "ENDPOINT_TIMEOUT"
	EndpointCheckConnectionRefused = "ENDPOINT_CONNECTION_REFUSED"
	EndpointCheckTLSFailed         = "ENDPOINT_TLS_FAILED"
	EndpointCheckUnreachable       = "ENDPOINT_UNREACHABLE"
	EndpointCheckRedirected        = "ENDPOINT_REDIRECTED"
	EndpointCheckUnexpectedHTTP    = "ENDPOINT_UNEXPECTED_HTTP"
	EndpointCheckNotPanel          = "ENDPOINT_NOT_PANEL"
	EndpointCheckPanelNotReady     = "ENDPOINT_PANEL_NOT_READY"
	EndpointCheckEntryInvalid      = "ENDPOINT_ENTRY_INVALID"
)

var (
	errEndpointProbeUnsafeAddress = errors.New("endpoint probe target is not allowed")
	errEndpointProbeNoAddress     = errors.New("endpoint probe target has no address")
)

type EndpointCheckResult struct {
	Endpoint      string `json:"endpoint"`
	Status        string `json:"status"`
	Code          string `json:"code"`
	Detail        string `json:"detail"`
	Reachable     bool   `json:"reachable"`
	PanelDetected bool   `json:"panelDetected"`
	Ready         bool   `json:"ready"`
}

type endpointHealthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

// CheckEndpoint verifies the URL from the controller's network perspective.
// It deliberately does not replace registration and heartbeat verification:
// cluster nodes can use one-way connectivity and may sit behind NAT or a proxy.
func CheckEndpoint(parent context.Context, raw string) EndpointCheckResult {
	endpoint := normalizeEndpoint(raw)
	result := EndpointCheckResult{Endpoint: endpoint, Status: EndpointCheckWarning}
	if !validEndpoint(endpoint) {
		result.Status = EndpointCheckInvalid
		result.Code = EndpointCheckInvalidURL
		result.Detail = "Panel 地址格式无效，请填写不含账号、查询参数或锚点的 HTTP/HTTPS 地址。"
		return result
	}

	parsed, _ := url.Parse(endpoint)
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	client := newEndpointProbeClient()
	defer client.CloseIdleConnections()
	healthURL := parsed.Scheme + "://" + parsed.Host + "/health/ready"
	healthBody, healthStatus, err := endpointProbeRequest(ctx, client, healthURL, 64<<10)
	if err != nil {
		result.Code, result.Detail = endpointProbeFailure(err)
		return result
	}
	result.Reachable = true
	if healthStatus >= http.StatusMultipleChoices && healthStatus < http.StatusBadRequest {
		result.Code = EndpointCheckRedirected
		result.Detail = "Panel 健康检查地址发生跳转，请填写跳转后的最终面板地址。"
		return result
	}

	var health endpointHealthResponse
	if json.Unmarshal(healthBody, &health) != nil || health.Checks == nil {
		result.Code = EndpointCheckNotPanel
		result.Detail = "目标地址可以访问，但未返回 OneinStack Panel 健康检查响应。"
		return result
	}
	_, hasDatabase := health.Checks["database"]
	_, hasWebUI := health.Checks["webui"]
	if !hasDatabase || !hasWebUI {
		result.Code = EndpointCheckNotPanel
		result.Detail = "目标地址可以访问，但无法确认它是 OneinStack Panel。"
		return result
	}
	result.PanelDetected = true
	if healthStatus != http.StatusOK || health.Status != "ok" || health.Checks["database"] != "ok" || health.Checks["webui"] != "ok" {
		result.Code = EndpointCheckPanelNotReady
		result.Detail = "已识别 OneinStack Panel，但数据库或前端资源尚未就绪。"
		return result
	}

	pageBody, pageStatus, err := endpointProbeRequest(ctx, client, endpoint, 512<<10)
	if err != nil {
		result.Code, result.Detail = endpointProbeFailure(err)
		return result
	}
	if pageStatus >= http.StatusMultipleChoices && pageStatus < http.StatusBadRequest {
		result.Code = EndpointCheckRedirected
		result.Detail = "面板地址发生跳转，请填写跳转后的最终地址。"
		return result
	}
	if pageStatus < http.StatusOK || pageStatus >= http.StatusMultipleChoices {
		result.Code = EndpointCheckUnexpectedHTTP
		result.Detail = "目标 Panel 已就绪，但填写的面板入口返回了异常 HTTP 状态。"
		return result
	}
	if !isPanelEntryPage(pageBody) {
		result.Code = EndpointCheckEntryInvalid
		result.Detail = "目标 Panel 已就绪，但填写的地址不是有效的 Panel 页面；如已启用安全入口，请补充正确入口路径。"
		return result
	}

	result.Status = EndpointCheckHealthy
	result.Code = EndpointCheckOK
	result.Detail = "地址检测通过，目标 OneinStack Panel 已就绪。"
	result.Ready = true
	return result
}

func newEndpointProbeClient() *http.Client {
	dialer := &net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
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
				if !allowedEndpointProbeAddress(candidate) {
					return nil, errEndpointProbeUnsafeAddress
				}
			}
			var lastErr error
			for _, candidate := range addresses {
				connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
				if dialErr == nil {
					return connection, nil
				}
				lastErr = dialErr
			}
			return nil, lastErr
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       15 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: 4 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func endpointProbeRequest(ctx context.Context, client *http.Client, target string, maxBody int64) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json,text/html;q=0.9")
	request.Header.Set("User-Agent", "OneinStack-Panel/cluster-endpoint-check")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBody))
	if err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

func allowedEndpointProbeAddress(address netip.Addr) bool {
	address = address.Unmap()
	return address.IsValid() && address.IsGlobalUnicast() &&
		!address.IsLoopback() && !address.IsLinkLocalUnicast() &&
		!address.IsMulticast() && !address.IsUnspecified()
}

func endpointProbeFailure(err error) (string, string) {
	switch {
	case errors.Is(err, errEndpointProbeUnsafeAddress):
		return EndpointCheckUnsafeAddress, "为避免访问本机、链路本地或保留服务地址，主控未对该地址发起检测。"
	case errors.Is(err, errEndpointProbeNoAddress):
		return EndpointCheckDNSFailed, "目标主机没有可用的 IP 地址，请检查域名和 DNS 配置。"
	case errors.Is(err, context.DeadlineExceeded):
		return EndpointCheckTimeout, "目标地址检测超时，请检查地址、端口、防火墙和网络访问策略。"
	case errors.Is(err, syscall.ECONNREFUSED):
		return EndpointCheckConnectionRefused, "目标地址拒绝连接，请确认 Panel 服务已启动且端口正确。"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return EndpointCheckDNSFailed, "目标地址解析失败，请检查域名和主控服务器的 DNS 配置。"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return EndpointCheckTimeout, "目标地址检测超时，请检查地址、端口、防火墙和网络访问策略。"
	}
	var tlsErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	if errors.As(err, &tlsErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostnameErr) || errors.As(err, &invalidCertificate) {
		return EndpointCheckTLSFailed, "目标 HTTPS 证书校验失败，请检查证书有效期、域名和证书链。"
	}
	return EndpointCheckUnreachable, "无法访问目标地址，请检查网络、端口、代理和防火墙配置。"
}

func isPanelEntryPage(body []byte) bool {
	lower := bytes.ToLower(body)
	return bytes.Contains(lower, []byte("<title>oneinstack</title>")) &&
		(bytes.Contains(lower, []byte(`id="app"`)) || bytes.Contains(lower, []byte(`id='app'`))) &&
		!bytes.Contains(lower, []byte("__panel-entry_hint.css"))
}
