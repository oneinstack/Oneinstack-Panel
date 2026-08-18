package certificate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newCatalogTest(t *testing.T) (*Catalog, *gorm.DB) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "certificate-catalog.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.Certificate{}, &models.ManagedCertificate{}, &models.CertificateBinding{}, &models.DNSAccount{}); err != nil {
		t.Fatal(err)
	}
	return NewCatalog(database, filepath.Join(t.TempDir(), "certificates"), nil), database
}

func TestCreateSelfSignedPersistsUsableMaterial(t *testing.T) {
	catalog, _ := newCatalogTest(t)
	record, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"example.com", "www.example.com"}, Algorithm: "rsa-2048"})
	if err != nil {
		t.Fatal(err)
	}
	if record.Provider != "self-signed" || record.Algorithm != "rsa-2048" || record.Status != models.CertificateStatusActive {
		t.Fatalf("unexpected record: %#v", record)
	}
	certificatePEM, _, err := catalog.ReadMaterial(record.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM, _, err := catalog.ReadMaterial(record.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateCertificateMaterial(certificatePEM, privateKeyPEM, []string{"example.com"}); err != nil {
		t.Fatalf("generated material is invalid: %v", err)
	}
}

func TestUploadRejectsDomainOutsideCertificate(t *testing.T) {
	catalog, _ := newCatalogTest(t)
	generated, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"example.com"}, Algorithm: "ec-256"})
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, _, _ := catalog.ReadMaterial(generated.ID, false)
	privateKeyPEM, _, _ := catalog.ReadMaterial(generated.ID, true)
	if _, err := catalog.CreateUpload(CreateCertificateOptions{Domains: []string{"other.example.com"}, CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM}); err == nil {
		t.Fatal("upload accepted a domain not covered by the certificate")
	}
}

func TestUploadInfersDomainsWhenRequestOmitsThem(t *testing.T) {
	catalog, _ := newCatalogTest(t)
	generated, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"inferred.example.com"}, Algorithm: "ec-256"})
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, _, _ := catalog.ReadMaterial(generated.ID, false)
	privateKeyPEM, _, _ := catalog.ReadMaterial(generated.ID, true)
	record, err := catalog.CreateUpload(CreateCertificateOptions{CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM})
	if err != nil {
		t.Fatal(err)
	}
	if record.Domains != "inferred.example.com" {
		t.Fatalf("inferred domains = %q", record.Domains)
	}
}

func TestDNSAccountCredentialsAreEncrypted(t *testing.T) {
	if err := utils.ConfigureCredentialKey(bytes.Repeat([]byte{0x42}, 32)); err != nil {
		t.Fatal(err)
	}
	catalog, database := newCatalogTest(t)
	account, err := catalog.SaveDNSAccount("", "primary", "cloudflare", "api-token", "zone-secret", true)
	if err != nil {
		t.Fatal(err)
	}
	if !account.CredentialConfigured || account.CredentialOne == "api-token" || account.CredentialTwo == "zone-secret" {
		t.Fatalf("credential was not encrypted: %#v", account)
	}
	var stored models.DNSAccount
	if err := database.First(&stored, "id = ?", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	value, err := utils.DecryptCredential(stored.CredentialOne, utils.CredentialPurposeCertificateDNS)
	if err != nil || value != "api-token" {
		t.Fatalf("decrypt credential = %q, err=%v", value, err)
	}
}

func TestDeleteRemovesManagedMaterial(t *testing.T) {
	catalog, _ := newCatalogTest(t)
	record, err := catalog.CreateSelfSigned(SelfSignedOptions{Domains: []string{"delete.example.com"}, Algorithm: "ec-256"})
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Dir(record.CertificatePath)
	if err := catalog.Delete(record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatalf("certificate directory still exists, err=%v", err)
	}
}
