package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

var releaseVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]?)\.(0|[1-9][0-9]?)$`)

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: verify-center-release-head.go RESPONSE_FILE")
	}
	publicKey, err := base64.StdEncoding.DecodeString(os.Getenv("CENTER_RELEASE_PUBLIC_KEY"))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("CENTER_RELEASE_PUBLIC_KEY must contain a trusted Ed25519 public key")
	}
	file, err := os.Open(os.Args[1])
	if err != nil {
		return err
	}
	defer file.Close()
	var document struct {
		KeyID     string `json:"keyId"`
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 16<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode Center version head: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("Center version head has trailing content")
	}
	keyDigest := sha256.Sum256(publicKey)
	if document.KeyID != hex.EncodeToString(keyDigest[:8]) {
		return fmt.Errorf("Center version head uses an untrusted key")
	}
	payload, err := base64.StdEncoding.DecodeString(document.Payload)
	if err != nil {
		return fmt.Errorf("decode Center version payload: %w", err)
	}
	signature, err := base64.StdEncoding.DecodeString(document.Signature)
	if err != nil || !ed25519.Verify(publicKey, payload, signature) {
		return fmt.Errorf("Center version head signature verification failed")
	}
	var head struct {
		SchemaVersion int       `json:"schemaVersion"`
		GeneratedAt   time.Time `json:"generatedAt"`
		Version       string    `json:"version"`
	}
	decoder = json.NewDecoder(io.LimitReader(bytes.NewReader(payload), 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&head); err != nil {
		return fmt.Errorf("decode signed Center version payload: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("signed Center version payload has trailing content")
	}
	if head.SchemaVersion != 1 || time.Since(head.GeneratedAt) > 5*time.Minute || time.Until(head.GeneratedAt) > time.Minute {
		return fmt.Errorf("signed Center version payload has an invalid schema or timestamp")
	}
	if head.Version != "" && !releaseVersionPattern.MatchString(head.Version) {
		return fmt.Errorf("signed Center version is invalid")
	}
	fmt.Println(head.Version)
	return nil
}
