package certificate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	BindingStatusActive   = "active"
	BindingStatusError    = "error"
	BindingStatusDisabled = "disabled"
)

type Catalog struct {
	db       *gorm.DB
	root     string
	deployer Deployer
}

type CertificateListOptions struct {
	Page     int
	PageSize int
}

type CertificateList struct {
	Data     []models.ManagedCertificate `json:"data"`
	Total    int64                       `json:"total"`
	Page     int                         `json:"page"`
	PageSize int                         `json:"pageSize"`
}

type CreateCertificateOptions struct {
	Domains         []string
	CertificatePEM  []byte
	PrivateKeyPEM   []byte
	Remark          string
	AutoRenew       bool
	RenewBeforeDays int
}

type SelfSignedOptions struct {
	Domains         []string
	Algorithm       string
	ValidityYears   int
	Remark          string
	AutoRenew       bool
	RenewBeforeDays int
}

type BindingResult struct {
	Certificate models.ManagedCertificate `json:"certificate"`
	Binding     models.CertificateBinding `json:"binding"`
}

type DNSProvider struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

var dnsProviders = []DNSProvider{
	{Value: "cloudflare", Label: "Cloudflare"},
	{Value: "aliyun", Label: "阿里云"},
	{Value: "tencentcloud", Label: "腾讯云"},
}

func NewCatalog(db *gorm.DB, root string, deployer Deployer) *Catalog {
	return &Catalog{db: db, root: filepath.Clean(root), deployer: deployer}
}

func SupportedDNSProviders() []DNSProvider {
	result := make([]DNSProvider, len(dnsProviders))
	copy(result, dnsProviders)
	return result
}

func IsSupportedDNSProvider(value string) bool {
	for _, provider := range dnsProviders {
		if provider.Value == value {
			return true
		}
	}
	return false
}

func (catalog *Catalog) ensureRoot() error {
	if catalog == nil || catalog.db == nil || invalidPathRoot(catalog.root) {
		return errors.New("certificate catalog is not configured")
	}
	if err := os.MkdirAll(catalog.root, 0700); err != nil {
		return fmt.Errorf("create certificate directory: %w", err)
	}
	return os.Chmod(catalog.root, 0700)
}

func (catalog *Catalog) CreateUpload(options CreateCertificateOptions) (*models.ManagedCertificate, error) {
	if err := catalog.ensureRoot(); err != nil {
		return nil, err
	}
	domains, err := normalizeManagedDomains(options.Domains)
	if err != nil && len(options.Domains) == 0 {
		block, _ := pem.Decode(options.CertificatePEM)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, errors.New("certificate must be PEM encoded")
		}
		leaf, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse certificate: %w", parseErr)
		}
		values := append([]string{}, leaf.DNSNames...)
		for _, ip := range leaf.IPAddresses {
			values = append(values, ip.String())
		}
		domains, err = normalizeManagedDomains(values)
	}
	if err != nil {
		return nil, err
	}
	metadata, algorithm, err := validateCertificateMaterial(options.CertificatePEM, options.PrivateKeyPEM, domains)
	if err != nil {
		return nil, err
	}
	return catalog.persistMaterial("uploaded", domains, options.CertificatePEM, options.PrivateKeyPEM, metadata, algorithm, options.Remark, options.AutoRenew, options.RenewBeforeDays)
}

func (catalog *Catalog) CreateSelfSigned(options SelfSignedOptions) (*models.ManagedCertificate, error) {
	if err := catalog.ensureRoot(); err != nil {
		return nil, err
	}
	domains, err := normalizeManagedDomains(options.Domains)
	if err != nil {
		return nil, err
	}
	if !IsSupportedKeyAlgorithm(options.Algorithm) {
		return nil, fmt.Errorf("unsupported certificate key algorithm %q", options.Algorithm)
	}
	if options.ValidityYears == 0 {
		options.ValidityYears = 10
	}
	if options.ValidityYears < 1 || options.ValidityYears > 30 {
		return nil, errors.New("self-signed certificate validity must be between 1 and 30 years")
	}
	privateKey, publicKey, algorithm, err := generatePrivateKey(options.Algorithm)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, bigIntLimit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: domains[0]},
		DNSNames:              dnsNames(domains),
		IPAddresses:           ipAddresses(domains),
		NotBefore:             now,
		NotAfter:              now.AddDate(options.ValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  false,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, fmt.Errorf("create self-signed certificate: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyPEM, err := marshalPrivateKey(privateKey, algorithm)
	if err != nil {
		return nil, err
	}
	return catalog.persistMaterial("self-signed", domains, certificatePEM, privateKeyPEM, template, algorithm, options.Remark, options.AutoRenew, options.RenewBeforeDays)
}

// bigIntLimit is deliberately large enough for a positive serial while still
// keeping the serial bounded and compatible with common certificate tooling.
var bigIntLimit = func() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return limit
}()

func (catalog *Catalog) persistMaterial(provider string, domains []string, certificatePEM, privateKeyPEM []byte, metadata *x509.Certificate, algorithm, remark string, autoRenew bool, renewBeforeDays int) (*models.ManagedCertificate, error) {
	if renewBeforeDays == 0 {
		renewBeforeDays = 30
	}
	if renewBeforeDays < 1 || renewBeforeDays > 90 {
		return nil, errors.New("renew-before days must be between 1 and 90")
	}
	id := uuid.NewString()
	directory := filepath.Join(catalog.root, "managed", id)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create certificate version directory: %w", err)
	}
	certificatePath := filepath.Join(directory, "fullchain.pem")
	privateKeyPath := filepath.Join(directory, "privkey.pem")
	if err := writeFileAtomic(certificatePath, certificatePEM, 0644); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	if err := writeFileAtomic(privateKeyPath, privateKeyPEM, 0600); err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	now := time.Now().UTC()
	record := &models.ManagedCertificate{
		ID: id, Provider: provider, Domains: strings.Join(domains, ","),
		CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
		SerialNumber: metadata.SerialNumber.String(), Issuer: metadata.Issuer.String(),
		Algorithm: algorithm, Status: certificateStatus(metadata.NotAfter, now, renewBeforeDays),
		AutoRenew: autoRenew, RenewBeforeDays: renewBeforeDays, NotBefore: metadata.NotBefore.UTC(),
		NotAfter: metadata.NotAfter.UTC(), Remark: truncate(remark, 512), CreatedAt: now, UpdatedAt: now,
	}
	if err := catalog.db.Create(record).Error; err != nil {
		_ = os.RemoveAll(directory)
		return nil, fmt.Errorf("save certificate metadata: %w", err)
	}
	return record, nil
}

func (catalog *Catalog) List(options CertificateListOptions) (*CertificateList, error) {
	if catalog == nil || catalog.db == nil {
		return nil, errors.New("certificate catalog is not configured")
	}
	page, pageSize := normalizePage(options.Page, options.PageSize)
	query := catalog.db.Model(&models.ManagedCertificate{})
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var data []models.ManagedCertificate
	if err := query.Order("created_at DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&data).Error; err != nil {
		return nil, err
	}
	return &CertificateList{Data: data, Total: total, Page: page, PageSize: pageSize}, nil
}

func (catalog *Catalog) Get(id string) (*models.ManagedCertificate, error) {
	var record models.ManagedCertificate
	if err := catalog.db.First(&record, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return nil, err
	}
	if record.Status != models.CertificateStatusDisabled && record.Status != models.CertificateStatusError {
		record.Status = certificateStatus(record.NotAfter, time.Now().UTC(), record.RenewBeforeDays)
	}
	return &record, nil
}

func (catalog *Catalog) ListBindings(id string) ([]models.CertificateBinding, error) {
	var bindings []models.CertificateBinding
	if err := catalog.db.Where("managed_certificate_id = ?", strings.TrimSpace(id)).Order("created_at ASC").Find(&bindings).Error; err != nil {
		return nil, err
	}
	return bindings, nil
}

func (catalog *Catalog) ReadMaterial(id string, privateKey bool) ([]byte, string, error) {
	record, err := catalog.Get(id)
	if err != nil {
		return nil, "", err
	}
	path := record.CertificatePath
	filename := "fullchain.pem"
	if privateKey {
		path = record.PrivateKeyPath
		filename = "privkey.pem"
	}
	if !isWithin(catalog.root, path) {
		return nil, "", errors.New("certificate material path is outside the managed directory")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	return content, filename, nil
}

func (catalog *Catalog) Delete(id string) error {
	var record models.ManagedCertificate
	if err := catalog.db.First(&record, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return err
	}
	var count int64
	if err := catalog.db.Model(&models.CertificateBinding{}).Where("managed_certificate_id = ? AND status = ?", record.ID, BindingStatusActive).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("certificate is still bound to a website")
	}
	if err := catalog.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("managed_id = ?", record.ID).Delete(&models.Certificate{}).Error; err != nil {
			return err
		}
		return tx.Delete(&record).Error
	}); err != nil {
		return err
	}
	return removeManagedDirectory(catalog.root, record.CertificatePath, record.PrivateKeyPath)
}

func (catalog *Catalog) Bind(ctx context.Context, id string, websiteID int64, forceHTTPS bool) (*BindingResult, error) {
	if catalog.deployer == nil {
		return nil, errors.New("certificate deployer is not configured")
	}
	record, err := catalog.Get(id)
	if err != nil {
		return nil, err
	}
	var website models.Website
	if err := catalog.db.First(&website, "id = ?", websiteID).Error; err != nil {
		return nil, err
	}
	if !website.Enabled {
		return nil, errors.New("website is disabled")
	}
	leaf, err := parseCertificateFile(record.CertificatePath)
	if err != nil {
		return nil, err
	}
	domains, err := certificateDomains(website.Domain)
	if err != nil {
		return nil, err
	}
	for _, domain := range domains {
		if err := leaf.VerifyHostname(domain); err != nil {
			return nil, fmt.Errorf("certificate does not cover website domain %s", domain)
		}
	}
	rollback, err := catalog.deployer.Deploy(ctx, websiteID, record.CertificatePath, record.PrivateKeyPath, forceHTTPS)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	binding := &models.CertificateBinding{ID: uuid.NewString(), ManagedCertificateID: record.ID, WebsiteID: websiteID, Status: BindingStatusActive, ForceHTTPS: forceHTTPS, DeployedAt: &now, CreatedAt: now, UpdatedAt: now}
	persistErr := catalog.db.Transaction(func(tx *gorm.DB) error {
		var existingBinding models.CertificateBinding
		if err := tx.Where("managed_certificate_id = ? AND website_id = ?", record.ID, websiteID).First(&existingBinding).Error; err == nil {
			binding.ID = existingBinding.ID
			if err := tx.Model(&existingBinding).Updates(map[string]any{
				"status": binding.Status, "force_https": binding.ForceHTTPS,
				"last_error": "", "deployed_at": binding.DeployedAt, "updated_at": now,
			}).Error; err != nil {
				return err
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := tx.Create(binding).Error; err != nil {
				return err
			}
		} else {
			return err
		}
		legacy := &models.Certificate{ID: uuid.NewString(), WebsiteID: websiteID, ManagedID: record.ID, Provider: record.Provider, Domains: record.Domains, CertificatePath: record.CertificatePath, PrivateKeyPath: record.PrivateKeyPath, SerialNumber: record.SerialNumber, Issuer: record.Issuer, Status: models.CertificateStatusActive, AutoRenew: record.AutoRenew, RenewBeforeDays: record.RenewBeforeDays, ForceHTTPS: forceHTTPS, NotBefore: record.NotBefore, NotAfter: record.NotAfter, CreatedAt: now, UpdatedAt: now}
		var current models.Certificate
		if err := tx.Where("website_id = ?", websiteID).First(&current).Error; err == nil {
			legacy.ID = current.ID
			legacy.CreatedAt = current.CreatedAt
			return tx.Save(legacy).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		return tx.Create(legacy).Error
	})
	if persistErr != nil {
		_ = rollback(context.Background())
		return nil, persistErr
	}
	return &BindingResult{Certificate: *record, Binding: *binding}, nil
}

func (catalog *Catalog) Unbind(ctx context.Context, id string, websiteID int64) error {
	if catalog.deployer == nil {
		return errors.New("certificate deployer is not configured")
	}
	var binding models.CertificateBinding
	if err := catalog.db.Where("managed_certificate_id = ? AND website_id = ? AND status = ?", id, websiteID, BindingStatusActive).First(&binding).Error; err != nil {
		return err
	}
	rollback, err := catalog.deployer.Disable(ctx, websiteID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := catalog.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&binding).Updates(map[string]any{"status": BindingStatusDisabled, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Model(&models.Certificate{}).Where("website_id = ? AND managed_id = ?", websiteID, id).Updates(map[string]any{"status": models.CertificateStatusDisabled, "auto_renew": false, "force_https": false}).Error
	}); err != nil {
		_ = rollback(context.Background())
		return err
	}
	return nil
}

func (catalog *Catalog) ListDNSAccounts() ([]models.DNSAccount, error) {
	var accounts []models.DNSAccount
	if err := catalog.db.Order("created_at DESC").Find(&accounts).Error; err != nil {
		return nil, err
	}
	return accounts, nil
}

func (catalog *Catalog) SaveDNSAccount(id, name, provider, credentialOne, credentialTwo string, enabled bool) (*models.DNSAccount, error) {
	name = strings.TrimSpace(name)
	provider = strings.TrimSpace(strings.ToLower(provider))
	if name == "" || len(name) > 128 {
		return nil, errors.New("DNS account name is required")
	}
	if !IsSupportedDNSProvider(provider) {
		return nil, errors.New("unsupported DNS provider")
	}
	if strings.TrimSpace(credentialOne) == "" && strings.TrimSpace(id) == "" {
		return nil, errors.New("DNS account credential is required")
	}
	var account models.DNSAccount
	if id != "" {
		if err := catalog.db.First(&account, "id = ?", id).Error; err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(credentialOne) != "" {
		encOne, err := utils.EncryptCredential(credentialOne, utils.CredentialPurposeCertificateDNS)
		if err != nil {
			return nil, err
		}
		account.CredentialOne = encOne
		if credentialTwo != "" {
			encTwo, err := utils.EncryptCredential(credentialTwo, utils.CredentialPurposeCertificateDNS)
			if err != nil {
				return nil, err
			}
			account.CredentialTwo = encTwo
		}
		account.CredentialConfigured = true
	}
	account.Name, account.Provider, account.Enabled = name, provider, enabled
	if account.ID == "" {
		account.ID = uuid.NewString()
	}
	if err := catalog.db.Save(&account).Error; err != nil {
		return nil, err
	}
	return &account, nil
}

func (catalog *Catalog) DeleteDNSAccount(id string) error {
	return catalog.db.Delete(&models.DNSAccount{}, "id = ?", strings.TrimSpace(id)).Error
}

func generatePrivateKey(value string) (any, any, string, error) {
	switch value {
	case "ec-256":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		return key, &key.PublicKey, value, err
	case "ec-384":
		key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
		return key, &key.PublicKey, value, err
	case "rsa-2048", "rsa-3072", "rsa-4096":
		bits := 2048
		if value == "rsa-3072" {
			bits = 3072
		}
		if value == "rsa-4096" {
			bits = 4096
		}
		key, err := rsa.GenerateKey(rand.Reader, bits)
		return key, &key.PublicKey, value, err
	default:
		return nil, nil, "", fmt.Errorf("unsupported certificate key algorithm %q", value)
	}
}

func marshalPrivateKey(key any, algorithm string) ([]byte, error) {
	if strings.HasPrefix(algorithm, "ec-") {
		der, err := x509.MarshalECPrivateKey(key.(*ecdsa.PrivateKey))
		if err != nil {
			return nil, fmt.Errorf("marshal EC private key: %w", err)
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
	}
	der := x509.MarshalPKCS1PrivateKey(key.(*rsa.PrivateKey))
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der}), nil
}

func normalizeManagedDomains(values []string) ([]string, error) {
	seen := make(map[string]struct{})
	result := make([]string, 0, len(values))
	for _, value := range values {
		domain := strings.ToLower(strings.TrimSpace(value))
		if domain == "" {
			continue
		}
		if len(domain) > 253 || strings.ContainsAny(domain, " \t\r\n") {
			return nil, errors.New("certificate domain is invalid")
		}
		if net.ParseIP(strings.TrimPrefix(domain, "*.")) == nil && !strings.Contains(domain, ".") && !strings.HasPrefix(domain, "*.") {
			return nil, fmt.Errorf("certificate domain %q is invalid", domain)
		}
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	if len(result) == 0 || len(result) > 100 {
		return nil, errors.New("certificate requires between 1 and 100 domains")
	}
	return result, nil
}

func validateCertificateMaterial(certificatePEM, privateKeyPEM []byte, domains []string) (*x509.Certificate, string, error) {
	if len(certificatePEM) == 0 || len(privateKeyPEM) == 0 {
		return nil, "", errors.New("certificate and private key are required")
	}
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, "", errors.New("certificate must be PEM encoded")
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, "", fmt.Errorf("parse certificate: %w", err)
	}
	now := time.Now().UTC()
	if leaf.NotAfter.Before(now) {
		return nil, "", errors.New("certificate is expired")
	}
	if leaf.NotBefore.After(now.Add(10 * time.Minute)) {
		return nil, "", errors.New("certificate is not valid yet")
	}
	if _, err := tls.X509KeyPair(certificatePEM, privateKeyPEM); err != nil {
		return nil, "", fmt.Errorf("certificate and private key do not match: %w", err)
	}
	for _, domain := range domains {
		if err := leaf.VerifyHostname(domain); err != nil {
			return nil, "", fmt.Errorf("certificate does not cover domain %s", domain)
		}
	}
	return leaf, publicKeyAlgorithm(leaf.PublicKey), nil
}

func parseCertificateFile(path string) (*x509.Certificate, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(content)
	if block == nil {
		return nil, errors.New("certificate file is invalid")
	}
	return x509.ParseCertificate(block.Bytes)
}

func publicKeyAlgorithm(key any) string {
	switch value := key.(type) {
	case *ecdsa.PublicKey:
		if value.Curve == elliptic.P384() {
			return "ec-384"
		}
		return "ec-256"
	case *rsa.PublicKey:
		return fmt.Sprintf("rsa-%d", value.N)
	default:
		return "unknown"
	}
}

func dnsNames(domains []string) []string {
	result := make([]string, 0, len(domains))
	for _, d := range domains {
		if net.ParseIP(strings.TrimPrefix(d, "*.")) == nil {
			result = append(result, d)
		}
	}
	return result
}
func ipAddresses(domains []string) []net.IP {
	result := make([]net.IP, 0)
	for _, d := range domains {
		if ip := net.ParseIP(d); ip != nil {
			result = append(result, ip)
		}
	}
	return result
}

func isWithin(root, path string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, pathAbs)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func removeManagedDirectory(root, certificatePath, privateKeyPath string) error {
	if !isWithin(root, certificatePath) || !isWithin(root, privateKeyPath) {
		return errors.New("certificate files are outside the managed directory")
	}
	return os.RemoveAll(filepath.Dir(certificatePath))
}
