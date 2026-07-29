package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	systemservice "oneinstack/internal/services/system"
)

func TestResolveInitPasswordFromFile(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("Str0ng!Secret\r\n"), 0600); err != nil {
		t.Fatal(err)
	}

	password, err := resolveInitPassword("", passwordFile)
	if err != nil {
		t.Fatalf("resolveInitPassword: %v", err)
	}
	if password != "Str0ng!Secret" {
		t.Fatalf("unexpected password %q", password)
	}
}

func TestResolveInitPasswordRejectsConflictingSources(t *testing.T) {
	_, err := resolveInitPassword("flag-secret", "secret-file")
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("expected mutually exclusive source error, got %v", err)
	}
}

func TestResolveInitPasswordRequiresSource(t *testing.T) {
	_, err := resolveInitPassword("", "")
	if err == nil || !strings.Contains(err.Error(), "is required") {
		t.Fatalf("expected required source error, got %v", err)
	}
}

func TestResolveInitPasswordRejectsOversizedFile(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte(strings.Repeat("x", 4097)), 0600); err != nil {
		t.Fatal(err)
	}

	_, err := resolveInitPassword("", passwordFile)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected file size error, got %v", err)
	}
}

func TestResolveInitPasswordRejectsPermissiveFile(t *testing.T) {
	passwordFile := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(passwordFile, []byte("Str0ng!Secret\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(passwordFile, 0644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveInitPassword("", passwordFile)
	if err == nil || !strings.Contains(err.Error(), "group or other users") {
		t.Fatalf("expected file permission error, got %v", err)
	}
}

func TestFormatPanelEntryOutputShowsCurrentAccessURL(t *testing.T) {
	output := formatPanelEntryOutput(&systemservice.PanelNetworkSettings{
		HTTPAccessURL:     "http://服务器IP:8089",
		PanelEntryEnabled: true,
		PanelAccessURL:    "http://服务器IP:8089/AbCd123456",
	})
	if !strings.Contains(output, "http://服务器IP:8089/AbCd123456") {
		t.Fatalf("unexpected output: %s", output)
	}
}

func TestFormatPanelEntryOutputShowsDefaultURLWhenDisabled(t *testing.T) {
	output := formatPanelEntryOutput(&systemservice.PanelNetworkSettings{
		HTTPAccessURL:  "http://服务器IP:8089",
		HTTPSEnabled:   true,
		HTTPSAccessURL: "https://服务器IP:8443",
	})
	if !strings.Contains(output, "http://服务器IP:8089") ||
		!strings.Contains(output, "https://服务器IP:8443") {
		t.Fatalf("unexpected output: %s", output)
	}
}
