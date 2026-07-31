package panelupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	KeysetSchemaVersion = 1
	MaxKeysetBytes      = 256 << 10
	MaxKeysetKeys       = 32

	KeyStatusStaged  = "staged"
	KeyStatusActive  = "active"
	KeyStatusRetired = "retired"
	KeyStatusRevoked = "revoked"
)

type TrustedKeyRecord struct {
	KeyID            string     `json:"keyId"`
	Algorithm        string     `json:"algorithm"`
	PublicKey        string     `json:"publicKey"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"createdAt"`
	ActivatedAt      *time.Time `json:"activatedAt,omitempty"`
	RetiredAt        *time.Time `json:"retiredAt,omitempty"`
	RevokedAt        *time.Time `json:"revokedAt,omitempty"`
	RevocationReason string     `json:"revocationReason,omitempty"`
}

type KeysetSignature struct {
	KeyID string `json:"keyId"`
	Value string `json:"value"`
}

type KeysetDocument struct {
	SchemaVersion int                `json:"schemaVersion"`
	Revision      uint64             `json:"revision"`
	GeneratedAt   time.Time          `json:"generatedAt"`
	ActiveKeyID   string             `json:"activeKeyId"`
	Keys          []TrustedKeyRecord `json:"keys"`
	Signatures    []KeysetSignature  `json:"signatures"`
}

type trustState struct {
	SchemaVersion int                `json:"schemaVersion"`
	Revision      uint64             `json:"revision"`
	GeneratedAt   time.Time          `json:"generatedAt"`
	ActiveKeyID   string             `json:"activeKeyId"`
	Keys          []TrustedKeyRecord `json:"keys"`
	Digest        string             `json:"digest"`
	UpdatedAt     time.Time          `json:"updatedAt"`
}

type KeyTrustResult struct {
	Source          string    `json:"source"`
	Revision        uint64    `json:"revision,omitempty"`
	ActiveKeyID     string    `json:"activeKeyId,omitempty"`
	TrustedKeyCount int       `json:"trustedKeyCount"`
	RevokedKeyCount int       `json:"revokedKeyCount"`
	UpdatedAt       time.Time `json:"updatedAt,omitempty"`
}

func RefreshKeyTrust(
	ctx context.Context,
	client *http.Client,
	config Config,
) (map[string]string, KeyTrustResult, error) {
	staticKeys := cloneTrustedKeys(config.TrustedKeys)
	if strings.TrimSpace(config.KeyStatusURL) == "" {
		return staticKeys, KeyTrustResult{
			Source: "static", TrustedKeyCount: len(staticKeys),
		}, nil
	}
	if strings.TrimSpace(config.TrustStatePath) == "" {
		return nil, KeyTrustResult{}, fmt.Errorf("%w: trust state path is required", ErrInvalidManifest)
	}

	state, exists, err := loadTrustState(config.TrustStatePath)
	if err != nil {
		return nil, KeyTrustResult{}, err
	}
	currentKeys := effectiveTrustedKeys(staticKeys, state.Keys)
	document, err := fetchKeyset(ctx, client, config.KeyStatusURL)
	if err != nil {
		return nil, KeyTrustResult{}, err
	}
	payload, digest, err := validateKeyset(document)
	if err != nil {
		return nil, KeyTrustResult{}, err
	}
	if err := verifyKeyset(document, payload, currentKeys); err != nil {
		return nil, KeyTrustResult{}, err
	}
	if exists {
		if document.Revision < state.Revision {
			return nil, KeyTrustResult{}, fmt.Errorf(
				"%w: signing keyset rollback from revision %d to %d",
				ErrInvalidManifest,
				state.Revision,
				document.Revision,
			)
		}
		if document.Revision == state.Revision && digest != state.Digest {
			return nil, KeyTrustResult{}, fmt.Errorf(
				"%w: signing keyset revision %d changed without a revision increase",
				ErrInvalidManifest,
				document.Revision,
			)
		}
		if err := rejectRemovedKeys(state.Keys, document.Keys); err != nil {
			return nil, KeyTrustResult{}, err
		}
	}

	if !exists || document.Revision > state.Revision {
		state = trustState{
			SchemaVersion: KeysetSchemaVersion,
			Revision:      document.Revision,
			GeneratedAt:   document.GeneratedAt.UTC(),
			ActiveKeyID:   document.ActiveKeyID,
			Keys:          append([]TrustedKeyRecord(nil), document.Keys...),
			Digest:        digest,
			UpdatedAt:     time.Now().UTC(),
		}
		if err := saveTrustState(config.TrustStatePath, state); err != nil {
			return nil, KeyTrustResult{}, err
		}
	}

	effective := effectiveTrustedKeys(staticKeys, document.Keys)
	revoked := 0
	for _, record := range document.Keys {
		if record.Status == KeyStatusRevoked {
			revoked++
		}
	}
	return effective, KeyTrustResult{
		Source:          "center",
		Revision:        document.Revision,
		ActiveKeyID:     document.ActiveKeyID,
		TrustedKeyCount: len(effective),
		RevokedKeyCount: revoked,
		UpdatedAt:       document.GeneratedAt.UTC(),
	}, nil
}

func fetchKeyset(ctx context.Context, client *http.Client, keyStatusURL string) (KeysetDocument, error) {
	if err := validateRemoteURL(keyStatusURL); err != nil {
		return KeysetDocument{}, fmt.Errorf("%w: Center key status URL: %v", ErrInvalidManifest, err)
	}
	if client == nil {
		client = secureHTTPClient(20 * time.Second)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, keyStatusURL, nil)
	if err != nil {
		return KeysetDocument{}, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return KeysetDocument{}, fmt.Errorf("download Center signing keyset: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return KeysetDocument{}, fmt.Errorf(
			"download Center signing keyset: unexpected HTTP status %d",
			response.StatusCode,
		)
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, MaxKeysetBytes+1))
	if err != nil {
		return KeysetDocument{}, fmt.Errorf("read Center signing keyset: %w", err)
	}
	if len(content) > MaxKeysetBytes {
		return KeysetDocument{}, fmt.Errorf("%w: signing keyset exceeds %d bytes", ErrInvalidManifest, MaxKeysetBytes)
	}
	var document KeysetDocument
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return KeysetDocument{}, fmt.Errorf("%w: decode signing keyset: %v", ErrInvalidManifest, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return KeysetDocument{}, err
	}
	return document, nil
}

func validateKeyset(document KeysetDocument) ([]byte, string, error) {
	if document.SchemaVersion != KeysetSchemaVersion || document.Revision < 1 {
		return nil, "", fmt.Errorf("%w: unsupported signing keyset metadata", ErrInvalidManifest)
	}
	if document.GeneratedAt.IsZero() || document.GeneratedAt.After(time.Now().UTC().Add(24*time.Hour)) {
		return nil, "", fmt.Errorf("%w: invalid signing keyset generatedAt", ErrInvalidManifest)
	}
	if len(document.Keys) < 1 || len(document.Keys) > MaxKeysetKeys {
		return nil, "", fmt.Errorf("%w: signing keyset must contain 1-%d keys", ErrInvalidManifest, MaxKeysetKeys)
	}
	if len(document.Signatures) < 1 || len(document.Signatures) > MaxKeysetKeys {
		return nil, "", fmt.Errorf("%w: signing keyset must contain 1-%d signatures", ErrInvalidManifest, MaxKeysetKeys)
	}
	records := make(map[string]TrustedKeyRecord, len(document.Keys))
	activeCount := 0
	for _, record := range document.Keys {
		publicKey, err := decodeTrustedKey(record.KeyID, record.PublicKey)
		if err != nil {
			return nil, "", err
		}
		if record.Algorithm != "Ed25519" || record.CreatedAt.IsZero() {
			return nil, "", fmt.Errorf("%w: signing key %q metadata is incomplete", ErrInvalidManifest, record.KeyID)
		}
		digest := sha256.Sum256(publicKey)
		if record.KeyID != hex.EncodeToString(digest[:8]) {
			return nil, "", fmt.Errorf("%w: signing key %q identifier does not match its public key", ErrInvalidManifest, record.KeyID)
		}
		if _, duplicate := records[record.KeyID]; duplicate {
			return nil, "", fmt.Errorf("%w: duplicate signing key %q", ErrInvalidManifest, record.KeyID)
		}
		switch record.Status {
		case KeyStatusStaged:
		case KeyStatusActive:
			activeCount++
			if record.ActivatedAt == nil {
				return nil, "", fmt.Errorf("%w: active signing key %q has no activation time", ErrInvalidManifest, record.KeyID)
			}
		case KeyStatusRetired:
			if record.ActivatedAt == nil || record.RetiredAt == nil {
				return nil, "", fmt.Errorf("%w: retired signing key %q metadata is incomplete", ErrInvalidManifest, record.KeyID)
			}
		case KeyStatusRevoked:
			if record.RevokedAt == nil || strings.TrimSpace(record.RevocationReason) == "" {
				return nil, "", fmt.Errorf("%w: revoked signing key %q metadata is incomplete", ErrInvalidManifest, record.KeyID)
			}
		default:
			return nil, "", fmt.Errorf("%w: signing key %q has invalid status", ErrInvalidManifest, record.KeyID)
		}
		records[record.KeyID] = record
	}
	active, exists := records[document.ActiveKeyID]
	if !exists || active.Status != KeyStatusActive || activeCount != 1 {
		return nil, "", fmt.Errorf("%w: signing keyset active key is invalid", ErrInvalidManifest)
	}
	signatureIDs := make(map[string]struct{}, len(document.Signatures))
	for _, signature := range document.Signatures {
		record, exists := records[signature.KeyID]
		if !exists || record.Status == KeyStatusRevoked {
			return nil, "", fmt.Errorf("%w: signing keyset contains an unusable signature", ErrInvalidManifest)
		}
		if _, duplicate := signatureIDs[signature.KeyID]; duplicate {
			return nil, "", fmt.Errorf("%w: duplicate signing keyset signature %q", ErrInvalidManifest, signature.KeyID)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature.Value))
		if err != nil || len(decoded) != ed25519.SignatureSize {
			return nil, "", fmt.Errorf("%w: invalid signing keyset signature %q", ErrInvalidManifest, signature.KeyID)
		}
		signatureIDs[signature.KeyID] = struct{}{}
	}
	payload, err := keysetPayload(document)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(payload)
	return payload, hex.EncodeToString(digest[:]), nil
}

func keysetPayload(document KeysetDocument) ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion int                `json:"schemaVersion"`
		Revision      uint64             `json:"revision"`
		GeneratedAt   time.Time          `json:"generatedAt"`
		ActiveKeyID   string             `json:"activeKeyId"`
		Keys          []TrustedKeyRecord `json:"keys"`
	}{
		SchemaVersion: document.SchemaVersion,
		Revision:      document.Revision,
		GeneratedAt:   document.GeneratedAt.UTC(),
		ActiveKeyID:   document.ActiveKeyID,
		Keys:          document.Keys,
	})
}

func verifyKeyset(document KeysetDocument, payload []byte, trustedKeys map[string]string) error {
	for _, signature := range document.Signatures {
		encodedKey, trusted := trustedKeys[signature.KeyID]
		if !trusted {
			continue
		}
		publicKey, err := decodeTrustedKey(signature.KeyID, encodedKey)
		if err != nil {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature.Value))
		if err == nil && ed25519.Verify(publicKey, payload, decoded) {
			return nil
		}
	}
	return fmt.Errorf("%w: signing keyset is not signed by a currently trusted key", ErrInvalidManifest)
}

func effectiveTrustedKeys(staticKeys map[string]string, records []TrustedKeyRecord) map[string]string {
	result := cloneTrustedKeys(staticKeys)
	for _, record := range records {
		if record.Status == KeyStatusRevoked {
			delete(result, record.KeyID)
			continue
		}
		result[record.KeyID] = record.PublicKey
	}
	return result
}

func rejectRemovedKeys(previous, current []TrustedKeyRecord) error {
	currentIDs := make(map[string]struct{}, len(current))
	for _, record := range current {
		currentIDs[record.KeyID] = struct{}{}
	}
	for _, record := range previous {
		if _, exists := currentIDs[record.KeyID]; !exists {
			return fmt.Errorf(
				"%w: signing key %q was removed instead of being retained as revoked",
				ErrInvalidManifest,
				record.KeyID,
			)
		}
	}
	return nil
}

func decodeTrustedKey(keyID, encoded string) (ed25519.PublicKey, error) {
	keyID = strings.TrimSpace(keyID)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if keyID == "" || err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: signing key %q is not a valid Ed25519 key", ErrInvalidManifest, keyID)
	}
	return ed25519.PublicKey(decoded), nil
}

func cloneTrustedKeys(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for keyID, publicKey := range source {
		result[strings.TrimSpace(keyID)] = strings.TrimSpace(publicKey)
	}
	return result
}

func loadTrustState(path string) (trustState, bool, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return trustState{}, false, nil
	}
	if err != nil {
		return trustState{}, false, fmt.Errorf("read panel signing trust state: %w", err)
	}
	var state trustState
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return trustState{}, false, fmt.Errorf("decode panel signing trust state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return trustState{}, false, err
	}
	if state.SchemaVersion != KeysetSchemaVersion || state.Revision < 1 ||
		state.GeneratedAt.IsZero() || state.ActiveKeyID == "" ||
		len(state.Keys) < 1 || len(state.Keys) > MaxKeysetKeys ||
		len(state.Digest) != sha256.Size*2 {
		return trustState{}, false, fmt.Errorf("%w: stored signing trust state is invalid", ErrInvalidManifest)
	}
	return state, true, nil
}

func saveTrustState(path string, state trustState) error {
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode panel signing trust state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create panel signing trust directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".trusted-keys-*.new")
	if err != nil {
		return fmt.Errorf("create panel signing trust state: %w", err)
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(content, '\n')); err != nil {
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
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("install panel signing trust state: %w", err)
	}
	return nil
}
