package panelbackup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEncryptedArchiveRoundTripAndRejectsWrongPassphrase(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "payload.tar.gz")
	encrypted := filepath.Join(root, "backup.onebak")
	decrypted := filepath.Join(root, "decrypted.tar.gz")
	const payload = "panel backup payload\n"
	const passphrase = "correct-horse-battery-staple"
	if err := os.WriteFile(source, []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	if err := encryptArchive(source, encrypted, passphrase, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := decryptArchive(encrypted, decrypted, "wrong-passphrase-value", 1<<20); !errors.Is(err, ErrInvalidPassphrase) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	if _, err := decryptArchive(encrypted, decrypted, passphrase, 1<<20); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != payload {
		t.Fatalf("decrypted payload = %q", content)
	}
}

func TestEncryptedArchiveRejectsTrailingData(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "payload.tar.gz")
	encrypted := filepath.Join(root, "backup.onebak")
	decrypted := filepath.Join(root, "decrypted.tar.gz")
	const passphrase = "correct-horse-battery-staple"
	if err := os.WriteFile(source, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := encryptArchive(source, encrypted, passphrase, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(encrypted, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("trailing")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := decryptArchive(encrypted, decrypted, passphrase, 1<<20); !errors.Is(err, ErrInvalidBackup) {
		t.Fatalf("trailing data error = %v", err)
	}
}
