package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const shutdownTimeout = 10 * time.Second

// PanelConfig describes the management-plane listeners. HTTP is intentionally
// always enabled so an optional HTTPS configuration cannot make the default
// IP-based access path unavailable.
type PanelConfig struct {
	BindAddress     string
	HTTPPort        string
	HTTPSEnabled    bool
	HTTPSPort       string
	CertificateFile string
	PrivateKeyFile  string
	TrustedProxies  []string
}

type CertificateInfo struct {
	NotBefore   time.Time
	NotAfter    time.Time
	DNSNames    []string
	IPAddresses []string
}

// ListenAddress validates a configured TCP port and returns an address suitable
// for net.Listen. It is retained for compatibility and listens on all interfaces.
func ListenAddress(port string) (string, error) {
	return NetworkAddress("", port)
}

// NetworkAddress validates an IP bind address and TCP port. An empty bind
// address is equivalent to all interfaces.
func NetworkAddress(bindAddress, port string) (string, error) {
	port = strings.TrimSpace(port)
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", fmt.Errorf("invalid server port %q", port)
	}
	bindAddress = strings.TrimSpace(bindAddress)
	if bindAddress != "" && net.ParseIP(bindAddress) == nil {
		return "", fmt.Errorf("bind address %q must be an IPv4 or IPv6 address", bindAddress)
	}
	return net.JoinHostPort(bindAddress, strconv.Itoa(number)), nil
}

func ValidatePanelConfig(config PanelConfig) error {
	httpAddress, err := NetworkAddress(config.BindAddress, config.HTTPPort)
	if err != nil {
		return fmt.Errorf("HTTP listener: %w", err)
	}
	if config.HTTPSEnabled {
		httpsAddress, err := NetworkAddress(config.BindAddress, config.HTTPSPort)
		if err != nil {
			return fmt.Errorf("HTTPS listener: %w", err)
		}
		if httpAddress == httpsAddress {
			return errors.New("HTTP and HTTPS ports must be different")
		}
		if _, err := ValidateTLSCertificate(config.CertificateFile, config.PrivateKeyFile, time.Now()); err != nil {
			return err
		}
	}
	for _, trustedProxy := range config.TrustedProxies {
		trustedProxy = strings.TrimSpace(trustedProxy)
		if trustedProxy == "" {
			return errors.New("trusted proxy entries must not be empty")
		}
		if net.ParseIP(trustedProxy) != nil {
			continue
		}
		if _, _, err := net.ParseCIDR(trustedProxy); err != nil {
			return fmt.Errorf("trusted proxy %q must be an IP address or CIDR range", trustedProxy)
		}
	}
	return nil
}

func ValidateTLSCertificate(certificateFile, privateKeyFile string, now time.Time) (*CertificateInfo, error) {
	certificateFile = strings.TrimSpace(certificateFile)
	privateKeyFile = strings.TrimSpace(privateKeyFile)
	if certificateFile == "" || privateKeyFile == "" {
		return nil, errors.New("HTTPS requires both a certificate file and private key file")
	}
	keyPair, err := tls.LoadX509KeyPair(certificateFile, privateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load HTTPS certificate and private key: %w", err)
	}
	if len(keyPair.Certificate) == 0 {
		return nil, errors.New("HTTPS certificate file contains no certificates")
	}
	certificate, err := x509.ParseCertificate(keyPair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse HTTPS certificate: %w", err)
	}
	if now.Before(certificate.NotBefore) {
		return nil, fmt.Errorf("HTTPS certificate is not valid before %s", certificate.NotBefore.Format(time.RFC3339))
	}
	if !now.Before(certificate.NotAfter) {
		return nil, fmt.Errorf("HTTPS certificate expired at %s", certificate.NotAfter.Format(time.RFC3339))
	}
	info := &CertificateInfo{
		NotBefore: certificate.NotBefore,
		NotAfter:  certificate.NotAfter,
		DNSNames:  append([]string(nil), certificate.DNSNames...),
	}
	for _, address := range certificate.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, address.String())
	}
	return info, nil
}

func NewHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0, // Upload handlers enforce request-specific limits.
		WriteTimeout:      0, // Streaming endpoints manage their own lifetime.
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

// RunHTTP starts the server and blocks until it fails or the context is
// cancelled. Context cancellation triggers a graceful shutdown.
func RunHTTP(ctx context.Context, address string, handler http.Handler) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	return ServeHTTP(ctx, NewHTTPServer(address, handler), listener)
}

// RunPanel starts the mandatory HTTP listener and, when configured, a separate
// HTTPS listener. All sockets and certificate material are prepared before
// either server starts accepting requests.
func RunPanel(ctx context.Context, config PanelConfig, handler http.Handler) error {
	if err := ValidatePanelConfig(config); err != nil {
		return err
	}
	httpAddress, _ := NetworkAddress(config.BindAddress, config.HTTPPort)
	httpListener, err := net.Listen("tcp", httpAddress)
	if err != nil {
		return fmt.Errorf("listen for HTTP on %s: %w", httpAddress, err)
	}

	type preparedServer struct {
		server   *http.Server
		listener net.Listener
		scheme   string
	}
	prepared := []preparedServer{{
		server: NewHTTPServer(httpAddress, handler), listener: httpListener, scheme: "HTTP",
	}}
	closePrepared := func() {
		for _, item := range prepared {
			_ = item.listener.Close()
		}
	}

	if config.HTTPSEnabled {
		keyPair, err := tls.LoadX509KeyPair(
			strings.TrimSpace(config.CertificateFile),
			strings.TrimSpace(config.PrivateKeyFile),
		)
		if err != nil {
			closePrepared()
			return fmt.Errorf("load HTTPS certificate and private key: %w", err)
		}
		httpsAddress, _ := NetworkAddress(config.BindAddress, config.HTTPSPort)
		tcpListener, err := net.Listen("tcp", httpsAddress)
		if err != nil {
			closePrepared()
			return fmt.Errorf("listen for HTTPS on %s: %w", httpsAddress, err)
		}
		tlsListener := tls.NewListener(tcpListener, &tls.Config{
			Certificates: []tls.Certificate{keyPair},
			MinVersion:   tls.VersionTLS12,
		})
		prepared = append(prepared, preparedServer{
			server: NewHTTPServer(httpsAddress, handler), listener: tlsListener, scheme: "HTTPS",
		})
	}

	errCh := make(chan error, len(prepared))
	for _, item := range prepared {
		item := item
		go func() {
			err := item.server.Serve(item.listener)
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				err = fmt.Errorf("%s listener: %w", item.scheme, err)
			}
			errCh <- err
		}()
	}

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errCh:
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, item := range prepared {
		if err := item.server.Shutdown(shutdownCtx); err != nil {
			_ = item.server.Close()
			if runErr == nil {
				runErr = fmt.Errorf("shutdown %s server: %w", item.scheme, err)
			}
		}
	}
	return runErr
}

// ServeHTTP is split from RunHTTP so the lifecycle can be tested with an
// ephemeral listener.
func ServeHTTP(ctx context.Context, httpServer *http.Server, listener net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}

		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// ProbeAddress verifies that a changed listener address can be reserved. The
// listener remains open until the caller closes it, allowing several candidate
// addresses to be checked as one transaction.
func ProbeAddress(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("probe listener %s: %w", address, err)
	}
	return listener, nil
}

// PeerIsTrustedProxy checks only the socket peer. Forwarded headers are never
// trusted merely because a request claims to originate from a different IP.
func PeerIsTrustedProxy(remoteAddress string, trustedProxies []string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err != nil {
		host = strings.TrimSpace(remoteAddress)
	}
	peer := net.ParseIP(host)
	if peer == nil {
		return false
	}
	for _, configured := range trustedProxies {
		configured = strings.TrimSpace(configured)
		if address := net.ParseIP(configured); address != nil {
			if address.Equal(peer) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(configured)
		if err == nil && network.Contains(peer) {
			return true
		}
	}
	return false
}

func RequestIsHTTPS(request *http.Request, trustedProxies []string) bool {
	if request == nil {
		return false
	}
	if request.TLS != nil {
		return true
	}
	if !PeerIsTrustedProxy(request.RemoteAddr, trustedProxies) {
		return false
	}
	forwardedProto := strings.TrimSpace(strings.Split(request.Header.Get("X-Forwarded-Proto"), ",")[0])
	return strings.EqualFold(forwardedProto, "https")
}

func ClientIP(request *http.Request, trustedProxies []string) string {
	if request == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(request.RemoteAddr)
	}
	if !PeerIsTrustedProxy(request.RemoteAddr, trustedProxies) {
		return host
	}
	candidates := []string{request.Header.Get("X-Real-IP")}
	candidates = append(candidates, strings.Split(request.Header.Get("X-Forwarded-For"), ",")...)
	for _, candidate := range candidates {
		if parsed := net.ParseIP(strings.TrimSpace(candidate)); parsed != nil {
			return parsed.String()
		}
	}
	return host
}
