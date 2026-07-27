package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		port    string
		want    string
		wantErr bool
	}{
		{name: "valid", port: "8089", want: ":8089"},
		{name: "trim spaces", port: " 443 ", want: ":443"},
		{name: "empty", port: "", wantErr: true},
		{name: "zero", port: "0", wantErr: true},
		{name: "too large", port: "65536", wantErr: true},
		{name: "address injection", port: "127.0.0.1:8089", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ListenAddress(test.port)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got address %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ListenAddress: %v", err)
			}
			if got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestNetworkAddressValidatesBindIP(t *testing.T) {
	address, err := NetworkAddress("192.0.2.8", "8089")
	if err != nil {
		t.Fatalf("NetworkAddress: %v", err)
	}
	if address != "192.0.2.8:8089" {
		t.Fatalf("unexpected address %q", address)
	}
	if _, err := NetworkAddress("panel.example.com", "8089"); err == nil {
		t.Fatal("expected hostname bind address to be rejected")
	}
}

func TestPanelConfigKeepsHTTPIndependentFromHTTPS(t *testing.T) {
	config := PanelConfig{
		BindAddress: "0.0.0.0",
		HTTPPort:    "8089",
		HTTPSPort:   "8443",
	}
	if err := ValidatePanelConfig(config); err != nil {
		t.Fatalf("HTTP-only config should be valid: %v", err)
	}
	config.HTTPSEnabled = true
	if err := ValidatePanelConfig(config); err == nil {
		t.Fatal("expected enabled HTTPS without certificate material to be rejected")
	}
}

func TestValidateTLSCertificateAndIPSubjectAlternativeName(t *testing.T) {
	now := time.Now().UTC()
	certificateFile, privateKeyFile := writeTestCertificate(t, now.Add(-time.Hour), now.Add(time.Hour))
	info, err := ValidateTLSCertificate(certificateFile, privateKeyFile, now)
	if err != nil {
		t.Fatalf("ValidateTLSCertificate: %v", err)
	}
	if len(info.IPAddresses) != 1 || info.IPAddresses[0] != "192.0.2.25" {
		t.Fatalf("unexpected certificate IP names: %+v", info.IPAddresses)
	}
	if _, err := ValidateTLSCertificate(certificateFile, privateKeyFile, now.Add(2*time.Hour)); err == nil {
		t.Fatal("expected expired certificate to be rejected")
	}
}

func TestRequestIsHTTPSTrustsOnlyConfiguredSocketPeer(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://panel.example.test", nil)
	request.RemoteAddr = "198.51.100.20:443"
	request.Header.Set("X-Forwarded-Proto", "https")
	if RequestIsHTTPS(request, nil) {
		t.Fatal("untrusted forwarded protocol must be ignored")
	}
	if !RequestIsHTTPS(request, []string{"198.51.100.0/24"}) {
		t.Fatal("trusted proxy HTTPS header should be accepted")
	}
}

func TestServeHTTPShutsDownWhenContextIsCancelled(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	httpServer := NewHTTPServer(listener.Addr().String(), handler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		done <- ServeHTTP(ctx, httpServer, listener)
	}()

	response, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		cancel()
		t.Fatalf("GET test server: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("expected status %d, got %d", http.StatusOK, response.StatusCode)
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeHTTP: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down after context cancellation")
	}
}

func writeTestCertificate(t *testing.T, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "panel.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"panel.test"},
		IPAddresses:  []net.IP{net.ParseIP("192.0.2.25")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificateFile := filepath.Join(directory, "panel.crt")
	privateKeyFile := filepath.Join(directory, "panel.key")
	if err := os.WriteFile(certificateFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(privateKeyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}), 0600); err != nil {
		t.Fatal(err)
	}
	return certificateFile, privateKeyFile
}
