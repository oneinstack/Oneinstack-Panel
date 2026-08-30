package certificate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-acme/lego/v4/challenge/dns01"
	"golang.org/x/crypto/acme"
)

var challengeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,512}$`)

type ProgressReporter func(progress int, message string)

const (
	ChallengeHTTP01 = "http-01"
	ChallengeDNS01  = "dns-01"
)

type IssueRequest struct {
	DirectoryURL   string
	Email          string
	Domains        []string
	AccountKeyPath string
	ChallengeRoot  string
	ChallengeType  string
	DNSProvider    DNSChallengeProvider
}

type IssuedCertificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

type Issuer interface {
	Issue(context.Context, IssueRequest, ProgressReporter) (*IssuedCertificate, error)
}

type ACMEIssuer struct {
	HTTPClient *http.Client
}

func (issuer *ACMEIssuer) Issue(
	ctx context.Context,
	request IssueRequest,
	report ProgressReporter,
) (*IssuedCertificate, error) {
	if err := validateIssueRequest(request); err != nil {
		return nil, err
	}
	request.ChallengeType = defaultChallengeType(request.ChallengeType)
	if request.ChallengeType == ChallengeHTTP01 {
		if err := validateHTTP01DNS(ctx, request.Domains); err != nil {
			return nil, err
		}
	}
	report = nonNilReporter(report)
	report(12, "正在加载 ACME 账户")
	accountKey, err := loadOrCreateECKey(request.AccountKeyPath)
	if err != nil {
		return nil, err
	}
	client := &acme.Client{
		Key:          accountKey,
		HTTPClient:   issuer.HTTPClient,
		DirectoryURL: request.DirectoryURL,
		UserAgent:    "OneinStack-Panel/ACME",
	}
	_, err = client.Register(ctx, &acme.Account{
		Contact: []string{"mailto:" + request.Email},
	}, acme.AcceptTOS)
	if err != nil && !errors.Is(err, acme.ErrAccountAlreadyExists) {
		return nil, fmt.Errorf("register ACME account: %w", err)
	}

	identifiers := make([]acme.AuthzID, 0, len(request.Domains))
	for _, domain := range request.Domains {
		identifiers = append(identifiers, acme.AuthzID{Type: "dns", Value: domain})
	}
	report(20, "正在创建证书订单")
	order, err := client.AuthorizeOrder(ctx, identifiers)
	if err != nil {
		return nil, fmt.Errorf("create ACME order: %w", err)
	}

	challengeDirectory := filepath.Join(filepath.Clean(request.ChallengeRoot), ".well-known", "acme-challenge")
	if request.ChallengeType == ChallengeHTTP01 {
		if err := os.MkdirAll(challengeDirectory, 0755); err != nil {
			return nil, fmt.Errorf("create ACME challenge directory: %w", err)
		}
	}
	for index, authorizationURL := range order.AuthzURLs {
		authorization, err := client.GetAuthorization(ctx, authorizationURL)
		if err != nil {
			return nil, fmt.Errorf("read ACME authorization: %w", err)
		}
		if authorization.Status == acme.StatusValid {
			continue
		}
		challenge := findChallenge(authorization, request.ChallengeType)
		if challenge == nil {
			return nil, fmt.Errorf("ACME server did not offer %s for %s", request.ChallengeType, authorization.Identifier.Value)
		}
		if !challengeTokenPattern.MatchString(challenge.Token) {
			return nil, errors.New("ACME server returned an unsafe challenge token")
		}
		challengePath := ""
		challengeResponse := ""
		if request.ChallengeType == ChallengeHTTP01 {
			challengeResponse, err = client.HTTP01ChallengeResponse(challenge.Token)
			if err != nil {
				return nil, fmt.Errorf("create ACME challenge response: %w", err)
			}
			challengePath = filepath.Join(challengeDirectory, challenge.Token)
			if err := writeFileAtomic(challengePath, []byte(challengeResponse), 0644); err != nil {
				return nil, fmt.Errorf("publish ACME challenge: %w", err)
			}
		} else {
			keyAuthorization, keyErr := acmeKeyAuthorization(accountKey, challenge.Token)
			if keyErr != nil {
				return nil, fmt.Errorf("create DNS challenge response: %w", keyErr)
			}
			challengeResponse = keyAuthorization
			if err := request.DNSProvider.Present(authorization.Identifier.Value, challenge.Token, keyAuthorization); err != nil {
				return nil, fmt.Errorf("publish DNS challenge: %w", err)
			}
			if err := waitForDNSChallenge(ctx, request.DNSProvider, authorization.Identifier.Value, keyAuthorization); err != nil {
				_ = request.DNSProvider.CleanUp(authorization.Identifier.Value, challenge.Token, keyAuthorization)
				return nil, fmt.Errorf("wait for DNS challenge propagation: %w", err)
			}
		}
		report(25+(index*35/max(1, len(order.AuthzURLs))), "正在验证域名 "+authorization.Identifier.Value)
		_, acceptErr := client.Accept(ctx, challenge)
		if acceptErr == nil {
			_, acceptErr = client.WaitAuthorization(ctx, authorizationURL)
		}
		var cleanupErr error
		if request.ChallengeType == ChallengeHTTP01 {
			cleanupErr = os.Remove(challengePath)
			if errors.Is(cleanupErr, os.ErrNotExist) {
				cleanupErr = nil
			}
		} else {
			cleanupErr = request.DNSProvider.CleanUp(authorization.Identifier.Value, challenge.Token, challengeResponse)
		}
		if acceptErr != nil {
			return nil, fmt.Errorf("validate domain %s: %w", authorization.Identifier.Value, acceptErr)
		}
		if cleanupErr != nil {
			return nil, fmt.Errorf("clean up %s challenge: %w", request.ChallengeType, cleanupErr)
		}
	}

	report(65, "域名验证完成，正在生成证书私钥")
	readyOrder, err := client.WaitOrder(ctx, order.URI)
	if err != nil {
		return nil, fmt.Errorf("wait for ACME order: %w", err)
	}
	certificateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate certificate private key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: request.Domains[0]},
		DNSNames: append([]string(nil), request.Domains...),
	}, certificateKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate request: %w", err)
	}
	report(78, "正在签发证书")
	chain, _, err := client.CreateOrderCert(ctx, readyOrder.FinalizeURL, csrDER, true)
	if err != nil {
		return nil, fmt.Errorf("finalize ACME order: %w", err)
	}
	if len(chain) == 0 {
		return nil, errors.New("ACME server returned an empty certificate chain")
	}
	var certificatePEM []byte
	for _, certificateDER := range chain {
		certificatePEM = append(certificatePEM, pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: certificateDER,
		})...)
	}
	keyDER, err := x509.MarshalECPrivateKey(certificateKey)
	if err != nil {
		return nil, fmt.Errorf("encode certificate private key: %w", err)
	}
	report(88, "证书签发完成，正在校验证书")
	return &IssuedCertificate{
		CertificatePEM: certificatePEM,
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{
			Type:  "EC PRIVATE KEY",
			Bytes: keyDER,
		}),
	}, nil
}

func validateIssueRequest(request IssueRequest) error {
	if strings.TrimSpace(request.DirectoryURL) == "" {
		return errors.New("ACME directory URL is required")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(request.Email))
	if err != nil || !strings.EqualFold(address.Address, strings.TrimSpace(request.Email)) {
		return errors.New("a valid ACME account email is required")
	}
	if len(request.Domains) == 0 || len(request.Domains) > 100 {
		return errors.New("between 1 and 100 certificate domains are required")
	}
	challengeType := strings.TrimSpace(strings.ToLower(request.ChallengeType))
	if challengeType == "" {
		challengeType = ChallengeHTTP01
	}
	if challengeType != ChallengeHTTP01 && challengeType != ChallengeDNS01 {
		return errors.New("unsupported ACME challenge type")
	}
	for _, domain := range request.Domains {
		if strings.TrimSpace(domain) == "" || (challengeType == ChallengeHTTP01 && strings.Contains(domain, "*")) {
			return errors.New("HTTP-01 certificate issuance does not support wildcard domains")
		}
	}
	paths := map[string]string{"account key": request.AccountKeyPath}
	if challengeType == ChallengeHTTP01 {
		paths["challenge root"] = request.ChallengeRoot
	}
	for label, value := range paths {
		cleaned := filepath.Clean(strings.TrimSpace(value))
		if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
			return fmt.Errorf("%s path must be a non-root absolute path", label)
		}
	}
	if challengeType == ChallengeDNS01 && request.DNSProvider == nil {
		return errors.New("DNS provider is required for DNS-01 issuance")
	}
	return nil
}

func validateHTTP01DNS(ctx context.Context, domains []string) error {
	for _, domain := range domains {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, domain)
		cancel()
		if err != nil {
			if isDNSNameNotFound(err) {
				return fmt.Errorf("HTTP-01 DNS lookup for %s failed: no A or AAAA record", domain)
			}
			return fmt.Errorf("HTTP-01 DNS lookup for %s failed: %w", domain, err)
		}
		if len(addresses) == 0 {
			return fmt.Errorf("HTTP-01 DNS lookup for %s failed: no A or AAAA record", domain)
		}
	}
	return nil
}

func isDNSNameNotFound(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "no such host") || strings.Contains(lower, "nxdomain")
}

func findChallenge(authorization *acme.Authorization, challengeType string) *acme.Challenge {
	if authorization == nil {
		return nil
	}
	for _, challenge := range authorization.Challenges {
		if challenge != nil && challenge.Type == challengeType {
			return challenge
		}
	}
	return nil
}

func acmeKeyAuthorization(key *ecdsa.PrivateKey, token string) (string, error) {
	thumbprint, err := acme.JWKThumbprint(key.Public())
	if err != nil {
		return "", err
	}
	return token + "." + thumbprint, nil
}

func waitForDNSChallenge(ctx context.Context, provider DNSChallengeProvider, domain, keyAuthorization string) error {
	timeout, interval := provider.Timeout()
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	fqdn, expected := dns01.GetRecord(domain, keyAuthorization)
	deadline := time.Now().Add(timeout)
	for {
		lookupCtx, cancel := context.WithTimeout(ctx, interval)
		records, lookupErr := net.DefaultResolver.LookupTXT(lookupCtx, fqdn)
		cancel()
		if lookupErr == nil {
			for _, record := range records {
				if record == expected {
					return nil
				}
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("DNS TXT record %s did not propagate within %s", fqdn, timeout)
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func loadOrCreateECKey(path string) (*ecdsa.PrivateKey, error) {
	content, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(content)
		if block == nil || block.Type != "EC PRIVATE KEY" {
			return nil, errors.New("stored ACME account key is invalid")
		}
		key, parseErr := x509.ParseECPrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse ACME account key: %w", parseErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read ACME account key: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ACME account key: %w", err)
	}
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("encode ACME account key: %w", err)
	}
	if err := writeFileAtomic(path, pem.EncodeToMemory(&pem.Block{
		Type: "EC PRIVATE KEY", Bytes: encoded,
	}), 0600); err != nil {
		return nil, fmt.Errorf("store ACME account key: %w", err)
	}
	return key, nil
}

func writeFileAtomic(path string, content []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".oneinstack-cert-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func nonNilReporter(report ProgressReporter) ProgressReporter {
	if report == nil {
		return func(int, string) {}
	}
	return report
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
