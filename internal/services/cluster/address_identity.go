package cluster

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"oneinstack/internal/models"
)

const (
	AddressIdentityPending       = "pending"
	AddressIdentityVerified      = "verified"
	AddressIdentityDifferentNode = "different_node"
	AddressIdentityUnreachable   = "unreachable"
	AddressIdentityUnsupported   = "unsupported"
)

// VerifyNodeAddress proves that the configured endpoint reaches the Panel
// whose public key was bound to this node by token-authenticated registration.
// The node token is never sent to the configured endpoint.
func (m *Manager) VerifyNodeAddress(parent context.Context, id uint) (models.ClusterNode, error) {
	node, err := m.GetNode(id)
	if err != nil {
		return node, err
	}
	if !validClusterPublicKey(node.IdentityPublicKey) || node.Status == models.ClusterNodeStatusPending || node.LastSeenAt == nil {
		return node, nil
	}
	status := probeNodeAddressIdentity(parent, node.Endpoint, node.IdentityPublicKey)
	now := time.Now()
	result := m.db.Model(&models.ClusterNode{}).
		Where("id = ? AND endpoint = ? AND identity_public_key = ? AND token_hash = ?", id, node.Endpoint, node.IdentityPublicKey, node.TokenHash).
		Updates(map[string]any{
			"address_identity_status":     status,
			"address_identity_checked_at": now,
			"address_identity_endpoint":   node.Endpoint,
		})
	if result.Error != nil {
		return node, result.Error
	}
	return m.GetNode(id)
}

func probeNodeAddressIdentity(parent context.Context, endpoint, publicKeyText string) string {
	if !validEndpoint(endpoint) {
		return AddressIdentityUnreachable
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return AddressIdentityUnreachable
	}
	publicKey, err := base64.StdEncoding.DecodeString(publicKeyText)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return AddressIdentityPending
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return AddressIdentityUnreachable
	}
	encoded, _ := json.Marshal(struct {
		Nonce string `json:"nonce"`
	}{Nonce: base64.StdEncoding.EncodeToString(nonce)})
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	client := newEndpointProbeClient()
	defer client.CloseIdleConnections()
	target := parsed.Scheme + "://" + parsed.Host + "/cluster/identity/challenge"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return AddressIdentityUnreachable
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return AddressIdentityUnreachable
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusMethodNotAllowed {
		return AddressIdentityUnsupported
	}
	if response.StatusCode != http.StatusOK {
		return AddressIdentityUnreachable
	}
	var body struct {
		Data struct {
			Signature string `json:"signature"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body); err != nil {
		return AddressIdentityUnsupported
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Data.Signature))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return AddressIdentityUnsupported
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), addressChallengeMessage(nonce), signature) {
		return AddressIdentityDifferentNode
	}
	return AddressIdentityVerified
}
