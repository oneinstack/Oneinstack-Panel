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
	"time"
)

const (
	translationBatchPath       = "/v1/panel/translation/batch"
	translationEnrollPath      = "/v1/panel/translation/enroll"
	translationSignatureDomain = "oneinstack-center-translation-v1"
	translationMaxRequestBytes = 128 << 10
)

var (
	ErrCenterUnavailable  = errors.New("translation Center is unavailable")
	ErrCenterUnauthorized = errors.New("translation Center rejected the Panel identity")
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
	baseURL  string
	http     *http.Client
	identity panelIdentity
	now      func() time.Time
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
	identity, created, err := loadPanelIdentity(cfg.InstallDir, cfg.IdentityPath)
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
		if err := removeActivationCode(activationPath); err != nil {
			return nil, err
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	} else if created {
		return nil, errors.New("translation identity requires a one-time Center activation code")
	}
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

func (c *centerClient) batch(ctx context.Context, sourceLocale, targetLocale string, texts []string) ([]centerResult, error) {
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
