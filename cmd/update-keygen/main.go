package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	keyID := flag.String("key-id", "", "Stable identifier stored in updateCenter.trustedKeys")
	privateOutput := flag.String("private-output", "update-signing.seed", "Output path for the private signing seed")
	publicOutput := flag.String("public-output", "update-signing.pub", "Output path for the Panel public key")
	flag.Parse()

	if err := generate(*keyID, *privateOutput, *publicOutput); err != nil {
		fmt.Fprintln(os.Stderr, "generate update signing key:", err)
		os.Exit(1)
	}
}

func generate(keyID, privateOutput, publicOutput string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" || strings.ContainsAny(keyID, " \t\r\n:") {
		return fmt.Errorf("key-id must be a non-empty identifier without whitespace or ':'")
	}
	privateOutput = filepath.Clean(strings.TrimSpace(privateOutput))
	publicOutput = filepath.Clean(strings.TrimSpace(publicOutput))
	if privateOutput == "." || publicOutput == "." || privateOutput == publicOutput {
		return fmt.Errorf("private-output and public-output must be distinct file paths")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	privateContent := []byte(base64.StdEncoding.EncodeToString(privateKey.Seed()) + "\n")
	publicContent := []byte(base64.StdEncoding.EncodeToString(publicKey) + "\n")
	if err := writeExclusive(privateOutput, privateContent, 0600); err != nil {
		return fmt.Errorf("write private seed: %w", err)
	}
	if err := writeExclusive(publicOutput, publicContent, 0644); err != nil {
		_ = os.Remove(privateOutput)
		return fmt.Errorf("write public key: %w", err)
	}
	fmt.Printf("Generated Ed25519 update key %q\n", keyID)
	fmt.Printf("Panel config: updateCenter.trustedKeys.%s: %s", keyID, publicContent)
	return nil
}

func writeExclusive(path string, content []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	removeOnFailure := true
	defer func() {
		if removeOnFailure {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	removeOnFailure = false
	return nil
}
