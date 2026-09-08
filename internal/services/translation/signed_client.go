package translation

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignedPanelClient exposes the existing Panel-Center identity for other
// narrowly-scoped, signed control-plane requests. It intentionally does not
// expose the private key.
type SignedPanelClient struct{ client *centerClient }

type SignedPanelClientConfig struct {
	BaseURL            string
	InstallDir         string
	IdentityPath       string
	ActivationCodeFile string
	Timeout            time.Duration
}

func NewSignedPanelClient(ctx context.Context, config SignedPanelClientConfig) (*SignedPanelClient, error) {
	client, err := newCenterClient(ctx, centerClientConfig{
		BaseURL: config.BaseURL, InstallDir: config.InstallDir,
		IdentityPath: config.IdentityPath, ActivationCodeFile: config.ActivationCodeFile,
		Timeout: config.Timeout,
	})
	if err != nil {
		return nil, err
	}
	return &SignedPanelClient{client: client}, nil
}

func (c *SignedPanelClient) InstanceID() string { return c.client.identity.instanceID }

func (c *SignedPanelClient) NewRequest(ctx context.Context, method, path, signatureDomain string, body []byte) (*http.Request, error) {
	if err := c.client.ensureRegistered(ctx); err != nil {
		return nil, err
	}
	timestamp := strconv.FormatInt(c.client.now().Unix(), 10)
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	digest := sha256.Sum256(body)
	canonical := strings.Join([]string{signatureDomain, strings.ToUpper(method), path, timestamp, nonce, hex.EncodeToString(digest[:])}, "\n")
	signature := ed25519.Sign(c.client.identity.privateKey, []byte(canonical))
	request, err := http.NewRequestWithContext(ctx, method, c.client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("X-Oneinstack-Panel-Instance-ID", c.client.identity.instanceID)
	request.Header.Set("X-Oneinstack-Panel-Key-ID", c.client.identity.keyID)
	request.Header.Set("X-Oneinstack-Panel-Timestamp", timestamp)
	request.Header.Set("X-Oneinstack-Panel-Nonce", nonce)
	request.Header.Set("X-Oneinstack-Panel-Signature", base64.StdEncoding.EncodeToString(signature))
	return request, nil
}

func (c *SignedPanelClient) Do(request *http.Request) (*http.Response, error) {
	return c.client.http.Do(request)
}
