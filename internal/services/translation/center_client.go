package translation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	translationBatchPath              = "/v1/panel/translation/batch"
	translationRegisterPath           = "/v1/panel/translation/register"
	translationEnrollPath             = "/v1/panel/translation/enroll"
	translationSignatureDomain        = "oneinstack-center-translation-v1"
	translationRegisterDomain         = "oneinstack-center-translation-register-v1"
	translationMaxRequestBytes        = 128 << 10
	translationRegisterStartupTimeout = 5 * time.Second
	translationRegisterMinRetryDelay  = time.Second
	translationRegisterMaxRetryDelay  = 5 * time.Minute
)

var (
	ErrCenterUnavailable  = errors.New("translation Center is unavailable")
	ErrCenterUnauthorized = errors.New("translation Center rejected the Panel identity")
	ErrCenterConflict     = errors.New("translation Center rejected the conflicting Panel identity")
	ErrCenterRateLimited  = errors.New("translation Center rate limited the request")
)

type centerClientConfig struct {
	BaseURL            string
	InstallDir         string
	IdentityPath       string
	ActivationCodeFile string
	Timeout            time.Duration
}

type centerClient struct {
	baseURL             string
	http                *http.Client
	identity            panelIdentity
	now                 func() time.Time
	registrationMu      sync.Mutex
	registered          bool
	registrationBlocked bool
	nextRegistrationAt  time.Time
	registrationDelay   time.Duration
}

type enrollRequest struct {
	SchemaVersion  int    `json:"schemaVersion"`
	InstanceID     string `json:"instanceId"`
	KeyID          string `json:"keyId"`
	PublicKey      string `json:"publicKey"`
	ActivationCode string `json:"activationCode"`
}

type enrollResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstanceID    string `json:"instanceId"`
	KeyID         string `json:"keyId"`
	Status        string `json:"status"`
}

type registerRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstanceID    string `json:"instanceId"`
	KeyID         string `json:"keyId"`
	PublicKey     string `json:"publicKey"`
	Timestamp     int64  `json:"timestamp"`
	Nonce         string `json:"nonce"`
	Signature     string `json:"signature"`
}

type registerResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	InstanceID    string `json:"instanceId"`
	KeyID         string `json:"keyId"`
	Status        string `json:"status"`
}

type batchRequest struct {
	SchemaVersion int      `json:"schemaVersion"`
	SourceLocale  string   `json:"sourceLocale"`
	TargetLocale  string   `json:"targetLocale"`
	Texts         []string `json:"texts"`
}

type batchResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Results       []centerResult `json:"results"`
}

type centerResult struct {
	Index      int    `json:"index"`
	Status     string `json:"status"`
	Translated string `json:"translated"`
}

func newCenterClient(ctx context.Context, cfg centerClientConfig) (*centerClient, error) {
	baseURL, err := validateCenterURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.InstallDir) == "" {
		return nil, errors.New("translation install directory is required")
	}
	identity, _, err := loadPanelIdentity(cfg.InstallDir, cfg.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("load Panel translation identity: %w", err)
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &centerClient{
		baseURL:  baseURL,
		http:     &http.Client{Timeout: timeout},
		identity: identity,
		now:      time.Now,
	}
	activationPath := resolveTranslationPath(cfg.InstallDir, cfg.ActivationCodeFile, defaultTranslationActivationPath, defaultTranslationActivationRelativePath)
	activationCode, readErr := readActivationCode(activationPath)
	if readErr == nil {
		if err := client.enroll(ctx, activationCode); err != nil {
			return nil, err
		}
		client.markRegistrationSuccess()
		if err := removeActivationCode(activationPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	registrationContext, cancel := context.WithTimeout(ctx, translationRegisterStartupTimeout)
	_ = client.ensureRegistered(registrationContext)
	cancel()
	return client, nil
}

func validateCenterURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackCenterHost(parsed.Hostname()))) {
		return "", errors.New("translation Center URL must use HTTPS (HTTP is allowed only for loopback development)")
	}
	return raw, nil
}

func isLoopbackCenterHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *centerClient) enroll(ctx context.Context, activationCode string) error {
	body, err := json.Marshal(enrollRequest{
		SchemaVersion:  1,
		InstanceID:     c.identity.instanceID,
		KeyID:          c.identity.keyID,
		PublicKey:      publicKeyText(c.identity.publicKey),
		ActivationCode: activationCode,
	})
	if err != nil {
		return fmt.Errorf("encode translation enrollment: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+translationEnrollPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create translation enrollment request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: enrollment request failed", ErrCenterUnavailable)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return ErrCenterUnauthorized
		}
		return ErrCenterUnavailable
	}
	var result enrollResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&result); err != nil {
		return fmt.Errorf("%w: invalid enrollment response", ErrCenterUnavailable)
	}
	if result.SchemaVersion != 1 || result.InstanceID != c.identity.instanceID ||
		result.KeyID != c.identity.keyID || result.Status != "active" {
		return fmt.Errorf("%w: enrollment response is invalid", ErrCenterUnavailable)
	}
	return nil
}

func (c *centerClient) registrationPayload(input registerRequest) []byte {
	return []byte(strings.Join([]string{
		translationRegisterDomain,
		http.MethodPost,
		translationRegisterPath,
		strconv.Itoa(input.SchemaVersion),
		strings.TrimSpace(input.InstanceID),
		strings.TrimSpace(input.KeyID),
		strings.TrimSpace(input.PublicKey),
		strconv.FormatInt(input.Timestamp, 10),
		strings.TrimSpace(input.Nonce),
	}, "\n"))
}

func (c *centerClient) register(ctx context.Context) error {
	timestamp := c.now().Unix()
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("%w: generate registration nonce", ErrCenterUnavailable)
	}
	input := registerRequest{
		SchemaVersion: 1,
		InstanceID:    c.identity.instanceID,
		KeyID:         c.identity.keyID,
		PublicKey:     publicKeyText(c.identity.publicKey),
		Timestamp:     timestamp,
		Nonce:         base64.RawURLEncoding.EncodeToString(nonceBytes),
	}
	input.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(c.identity.privateKey, c.registrationPayload(input)))
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("%w: encode automatic enrollment", ErrCenterUnavailable)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+translationRegisterPath, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: create automatic enrollment request", ErrCenterUnavailable)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: automatic enrollment request failed", ErrCenterUnavailable)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrCenterUnauthorized
	case http.StatusConflict:
		return ErrCenterConflict
	case http.StatusTooManyRequests:
		return ErrCenterRateLimited
	case http.StatusServiceUnavailable:
		return ErrCenterUnavailable
	case http.StatusOK, http.StatusCreated:
	default:
		return fmt.Errorf("%w: automatic enrollment returned HTTP status %d", ErrCenterUnavailable, response.StatusCode)
	}
	var result registerResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&result); err != nil {
		return fmt.Errorf("%w: invalid automatic enrollment response", ErrCenterUnavailable)
	}
	if result.SchemaVersion != 1 || result.InstanceID != c.identity.instanceID ||
		result.KeyID != c.identity.keyID || result.Status != "active" {
		return fmt.Errorf("%w: automatic enrollment response is invalid", ErrCenterUnavailable)
	}
	return nil
}

func (c *centerClient) markRegistrationSuccess() {
	c.registrationMu.Lock()
	defer c.registrationMu.Unlock()
	c.registered = true
	c.registrationBlocked = false
	c.nextRegistrationAt = time.Time{}
	c.registrationDelay = 0
}

func (c *centerClient) ensureRegistered(ctx context.Context) error {
	c.registrationMu.Lock()
	defer c.registrationMu.Unlock()
	if c.registered {
		return nil
	}
	if c.registrationBlocked {
		return ErrCenterUnauthorized
	}
	now := c.now()
	if !c.nextRegistrationAt.IsZero() && now.Before(c.nextRegistrationAt) {
		return ErrCenterUnavailable
	}
	err := c.register(ctx)
	if err == nil {
		c.registered = true
		c.nextRegistrationAt = time.Time{}
		c.registrationDelay = 0
		return nil
	}
	if errors.Is(err, ErrCenterUnauthorized) || errors.Is(err, ErrCenterConflict) {
		c.registrationBlocked = true
		return err
	}
	delay := c.registrationDelay
	if delay <= 0 {
		delay = translationRegisterMinRetryDelay
	} else if delay < translationRegisterMaxRetryDelay {
		delay *= 2
		if delay > translationRegisterMaxRetryDelay {
			delay = translationRegisterMaxRetryDelay
		}
	}
	c.registrationDelay = delay
	c.nextRegistrationAt = now.Add(delay)
	return err
}

func (c *centerClient) batch(ctx context.Context, sourceLocale, targetLocale string, texts []string) ([]centerResult, error) {
	if err := c.ensureRegistered(ctx); err != nil {
		return nil, err
	}
	body, err := json.Marshal(batchRequest{
		SchemaVersion: 1,
		SourceLocale:  sourceLocale,
		TargetLocale:  targetLocale,
		Texts:         texts,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: encode batch request", ErrCenterUnavailable)
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("%w: generate request nonce", ErrCenterUnavailable)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	digest := sha256.Sum256(body)
	canonical := strings.Join([]string{
		translationSignatureDomain,
		http.MethodPost,
		translationBatchPath,
		timestamp,
		nonce,
		hex.EncodeToString(digest[:]),
	}, "\n")
	signature := ed25519.Sign(c.identity.privateKey, []byte(canonical))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+translationBatchPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("%w: create batch request", ErrCenterUnavailable)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Oneinstack-Panel-Instance-ID", c.identity.instanceID)
	request.Header.Set("X-Oneinstack-Panel-Key-ID", c.identity.keyID)
	request.Header.Set("X-Oneinstack-Panel-Timestamp", timestamp)
	request.Header.Set("X-Oneinstack-Panel-Nonce", nonce)
	request.Header.Set("X-Oneinstack-Panel-Signature", base64.StdEncoding.EncodeToString(signature))
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: batch request failed", ErrCenterUnavailable)
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrCenterUnauthorized
	case http.StatusTooManyRequests:
		return nil, ErrCenterRateLimited
	case http.StatusServiceUnavailable:
		return nil, ErrCenterUnavailable
	case http.StatusOK:
	default:
		return nil, ErrCenterUnavailable
	}
	var result batchResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, translationMaxRequestBytes))
	if err := decoder.Decode(&result); err != nil || result.SchemaVersion != 1 || len(result.Results) != len(texts) {
		return nil, fmt.Errorf("%w: invalid batch response", ErrCenterUnavailable)
	}
	seen := make([]bool, len(texts))
	for _, item := range result.Results {
		if item.Index < 0 || item.Index >= len(texts) || seen[item.Index] {
			return nil, fmt.Errorf("%w: invalid batch result index", ErrCenterUnavailable)
		}
		seen[item.Index] = true
	}
	return result.Results, nil
}
