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
	"net/http"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/crypto/acme"
)

var challengeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,512}$`)

type ProgressReporter func(progress int, message string)

type IssueRequest struct {
	DirectoryURL   string
	Email          string
	Domains        []string
	AccountKeyPath string
	ChallengeRoot  string
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

	challengeDirectory := filepath.Join(
		filepath.Clean(request.ChallengeRoot),
		".well-known",
		"acme-challenge",
	)
	if err := os.MkdirAll(challengeDirectory, 0755); err != nil {
		return nil, fmt.Errorf("create ACME challenge directory: %w", err)
	}
	for index, authorizationURL := range order.AuthzURLs {
		authorization, err := client.GetAuthorization(ctx, authorizationURL)
		if err != nil {
			return nil, fmt.Errorf("read ACME authorization: %w", err)
		}
		if authorization.Status == acme.StatusValid {
			continue
		}
		challenge := findHTTPChallenge(authorization)
		if challenge == nil {
			return nil, fmt.Errorf("ACME server did not offer http-01 for %s", authorization.Identifier.Value)
		}
		if !challengeTokenPattern.MatchString(challenge.Token) {
			return nil, errors.New("ACME server returned an unsafe challenge token")
		}
		response, err := client.HTTP01ChallengeResponse(challenge.Token)
		if err != nil {
			return nil, fmt.Errorf("create ACME challenge response: %w", err)
		}
		challengePath := filepath.Join(challengeDirectory, challenge.Token)
		if err := writeFileAtomic(challengePath, []byte(response), 0644); err != nil {
			return nil, fmt.Errorf("publish ACME challenge: %w", err)
		}
		report(25+(index*35/max(1, len(order.AuthzURLs))), "正在验证域名 "+authorization.Identifier.Value)
		_, acceptErr := client.Accept(ctx, challenge)
		if acceptErr == nil {
			_, acceptErr = client.WaitAuthorization(ctx, authorizationURL)
		}
		removeErr := os.Remove(challengePath)
		if acceptErr != nil {
			return nil, fmt.Errorf("validate domain %s: %w", authorization.Identifier.Value, acceptErr)
		}
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, fmt.Errorf("remove ACME challenge: %w", removeErr)
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
	for _, domain := range request.Domains {
		if strings.TrimSpace(domain) == "" || strings.Contains(domain, "*") {
			return errors.New("HTTP-01 certificate issuance does not support wildcard domains")
		}
	}
	for label, value := range map[string]string{
		"account key":    request.AccountKeyPath,
		"challenge root": request.ChallengeRoot,
	} {
		cleaned := filepath.Clean(strings.TrimSpace(value))
		if !filepath.IsAbs(cleaned) || cleaned == string(filepath.Separator) {
			return fmt.Errorf("%s path must be a non-root absolute path", label)
		}
	}
	return nil
}

func findHTTPChallenge(authorization *acme.Authorization) *acme.Challenge {
	if authorization == nil {
		return nil
	}
	for _, challenge := range authorization.Challenges {
		if challenge != nil && challenge.Type == "http-01" {
			return challenge
		}
	}
	return nil
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
