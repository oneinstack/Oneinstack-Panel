package certificate

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateACMEAccountKeyIsStableAndPrivate(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "accounts", "test.key")
	first, err := loadOrCreateECKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateECKey(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.D.Cmp(second.D) != 0 {
		t.Fatal("ACME account key changed after reload")
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("account key permissions = %04o, want 0600", info.Mode().Perm())
	}
	content, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(content)
	if block == nil {
		t.Fatal("account key is not PEM")
	}
	if _, err := x509.ParseECPrivateKey(block.Bytes); err != nil {
		t.Fatalf("account key is invalid: %v", err)
	}
}

func TestValidateIssueRequestRejectsUnsafeHTTP01Inputs(t *testing.T) {
	valid := IssueRequest{
		DirectoryURL:   "https://acme.test/directory",
		Email:          "admin@example.com",
		Domains:        []string{"example.com"},
		AccountKeyPath: "/tmp/account.key",
		ChallengeRoot:  "/tmp/challenges",
	}
	if err := validateIssueRequest(valid); err != nil {
		t.Fatalf("valid request was rejected: %v", err)
	}
	cases := []IssueRequest{
		func() IssueRequest {
			value := valid
			value.Email = "not-an-email"
			return value
		}(),
		func() IssueRequest {
			value := valid
			value.Domains = []string{"*.example.com"}
			return value
		}(),
		func() IssueRequest {
			value := valid
			value.AccountKeyPath = "../account.key"
			return value
		}(),
		func() IssueRequest {
			value := valid
			value.ChallengeRoot = "/"
			return value
		}(),
	}
	for index, request := range cases {
		if err := validateIssueRequest(request); err == nil {
			t.Fatalf("unsafe case %d unexpectedly passed validation", index)
		}
	}
}
