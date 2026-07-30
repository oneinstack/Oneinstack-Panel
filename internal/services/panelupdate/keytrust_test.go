package panelupdate

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type trustTestKey struct {
	id      string
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func newTrustTestKey(t *testing.T) trustTestKey {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(publicKey)
	return trustTestKey{
		id:      hex.EncodeToString(digest[:8]),
		public:  publicKey,
		private: privateKey,
	}
}

func signedTrustDocument(
	t *testing.T,
	revision uint64,
	generatedAt time.Time,
	activeKeyID string,
	records []TrustedKeyRecord,
	signers ...trustTestKey,
) KeysetDocument {
	t.Helper()
	document := KeysetDocument{
		SchemaVersion: KeysetSchemaVersion,
		Revision:      revision,
		GeneratedAt:   generatedAt,
		ActiveKeyID:   activeKeyID,
		Keys:          append([]TrustedKeyRecord(nil), records...),
	}
	payload, err := keysetPayload(document)
	if err != nil {
		t.Fatal(err)
	}
	for _, signer := range signers {
		document.Signatures = append(document.Signatures, KeysetSignature{
			KeyID: signer.id,
			Value: base64.StdEncoding.EncodeToString(ed25519.Sign(signer.private, payload)),
		})
	}
	return document
}

func TestRefreshKeyTrustRotationAndRollbackProtection(t *testing.T) {
	oldKey := newTrustTestKey(t)
	newKey := newTrustTestKey(t)
	createdAt := time.Now().UTC().Add(-time.Hour)
	activatedAt := createdAt
	var (
		mu       sync.RWMutex
		document KeysetDocument
	)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		mu.RLock()
		defer mu.RUnlock()
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(document)
	}))
	defer server.Close()
	config := Config{
		KeyStatusURL:   server.URL,
		TrustStatePath: filepath.Join(t.TempDir(), "trusted-keys.json"),
		TrustedKeys: map[string]string{
			oldKey.id: base64.StdEncoding.EncodeToString(oldKey.public),
		},
	}

	stagedAt := time.Now().UTC()
	records := []TrustedKeyRecord{
		{
			KeyID: oldKey.id, Algorithm: "Ed25519",
			PublicKey: base64.StdEncoding.EncodeToString(oldKey.public),
			Status:    KeyStatusActive, CreatedAt: createdAt, ActivatedAt: &activatedAt,
		},
		{
			KeyID: newKey.id, Algorithm: "Ed25519",
			PublicKey: base64.StdEncoding.EncodeToString(newKey.public),
			Status:    KeyStatusStaged, CreatedAt: stagedAt,
		},
	}
	document = signedTrustDocument(t, 2, stagedAt, oldKey.id, records, oldKey, newKey)
	trusted, result, err := RefreshKeyTrust(t.Context(), server.Client(), config)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision != 2 || len(trusted) != 2 {
		t.Fatalf("staged key was not learned: result=%#v keys=%#v", result, trusted)
	}

	rotationAt := stagedAt.Add(time.Minute)
	records[0].Status = KeyStatusRetired
	records[0].RetiredAt = &rotationAt
	records[1].Status = KeyStatusActive
	records[1].ActivatedAt = &rotationAt
	document = signedTrustDocument(t, 3, rotationAt, newKey.id, records, oldKey, newKey)
	if _, result, err = RefreshKeyTrust(t.Context(), server.Client(), config); err != nil || result.ActiveKeyID != newKey.id {
		t.Fatalf("activate key: result=%#v err=%v", result, err)
	}
	preRevocation := document

	revokedAt := rotationAt.Add(time.Minute)
	records[0].Status = KeyStatusRevoked
	records[0].RevokedAt = &revokedAt
	records[0].RevocationReason = "rotation completed"
	document = signedTrustDocument(t, 4, revokedAt, newKey.id, records, newKey)
	trusted, result, err = RefreshKeyTrust(t.Context(), server.Client(), config)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := trusted[oldKey.id]; exists || result.RevokedKeyCount != 1 {
		t.Fatalf("revoked key remained trusted: result=%#v keys=%#v", result, trusted)
	}

	document = preRevocation
	_, _, err = RefreshKeyTrust(t.Context(), server.Client(), config)
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("expected rollback rejection, got %v", err)
	}
}
