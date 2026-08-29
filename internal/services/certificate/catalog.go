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

type ACMECertificateOptions struct {
	Domains            []string
	CertificatePEM     []byte
	PrivateKeyPEM      []byte
	Email              string
	DirectoryURL       string
	ChallengeType      string
	DNSAccountID       string
	ChallengeWebsiteID int64
	Metadata           *x509.Certificate
	AutoRenew          bool
	RenewBeforeDays    int
	Remark             string
}

type ACMERenewalOptions struct {
	CertificatePEM     []byte
	PrivateKeyPEM      []byte
	Metadata           *x509.Certificate
	Email              string
	DirectoryURL       string
	ChallengeType      string
	DNSAccountID       string
	ChallengeWebsiteID int64
	AutoRenew          bool
	RenewBeforeDays    int
}

// RequestValidationError identifies a certificate request field that can be
// corrected by the caller. These errors must be returned before a task is
// queued so the UI can restore the submit button and show the reason.
type RequestValidationError struct {
	Field   string
	Message string
}

func (err *RequestValidationError) Error() string {
	if err == nil {
		return "certificate request is invalid"
	}
	return err.Message
}

type BindingResult struct {
	Certificate models.ManagedCertificate `json:"certificate"`
	Binding     models.CertificateBinding `json:"binding"`
}

type DNSProvider struct {
	Value                 string `json:"value"`
	Label                 string `json:"label"`
	CredentialOneLabel    string `json:"credentialOneLabel"`
	CredentialTwoLabel    string `json:"credentialTwoLabel,omitempty"`
	CredentialTwoRequired bool   `json:"credentialTwoRequired"`
}

var dnsProviders = []DNSProvider{
	{Value: "cloudflare", Label: "Cloudflare", CredentialOneLabel: "API Token"},
	{Value: "aliyun", Label: "阿里云", CredentialOneLabel: "AccessKey ID", CredentialTwoLabel: "AccessKey Secret", CredentialTwoRequired: true},
	{Value: "tencentcloud", Label: "腾讯云", CredentialOneLabel: "SecretId", CredentialTwoLabel: "SecretKey", CredentialTwoRequired: true},
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
	options, err := normalizeSelfSignedOptions(options)
	if err != nil {
		return nil, err
	}
	if err := catalog.ensureRoot(); err != nil {
		return nil, err
	}
	domains := options.Domains
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

func (catalog *Catalog) CreateACME(options ACMECertificateOptions) (*models.ManagedCertificate, error) {
	if err := catalog.ensureRoot(); err != nil {
		return nil, err
	}
	domains, err := normalizeACMEDomains(options.Domains, defaultChallengeType(options.ChallengeType))
	if err != nil {
		return nil, err
	}
	metadata, algorithm, err := validateCertificateMaterial(options.CertificatePEM, options.PrivateKeyPEM, domains)
	if err != nil {
		return nil, err
	}
	if options.Metadata != nil {
		metadata = options.Metadata
	}
	return catalog.persistMaterialWithACME("acme", domains, options.CertificatePEM, options.PrivateKeyPEM, metadata, algorithm, options.Email, options.DirectoryURL, defaultChallengeType(options.ChallengeType), options.DNSAccountID, options.ChallengeWebsiteID, options.Remark, options.AutoRenew, options.RenewBeforeDays)
}

func (catalog *Catalog) RenewACME(ctx context.Context, id string, options ACMERenewalOptions) (*models.ManagedCertificate, error) {
	record, err := catalog.Get(id)
	if err != nil {
		return nil, err
	}
	if record.Provider != "acme" {
		return nil, errors.New("only ACME certificates can be renewed")
	}
	if record.Status == models.CertificateStatusDisabled {
		return nil, errors.New("disabled certificate cannot be renewed")
	}
	challengeType := strings.TrimSpace(options.ChallengeType)
	if challengeType == "" {
		challengeType = record.ChallengeType
	}
	challengeType, err = normalizeChallengeType(challengeType)
	if err != nil {
		return nil, err
	}
	domains, err := normalizeACMEDomains(taskDomains(record.Domains), challengeType)
	if err != nil {
		return nil, err
	}
	if options.Email == "" {
		options.Email = record.Email
	}
	if options.DirectoryURL == "" {
		options.DirectoryURL = record.DirectoryURL
	}
	if options.DNSAccountID == "" {
		options.DNSAccountID = record.DNSAccountID
	}
	if options.RenewBeforeDays == 0 {
		options.RenewBeforeDays = record.RenewBeforeDays
	}
	if options.RenewBeforeDays == 0 {
		options.RenewBeforeDays = 30
	}
	if options.RenewBeforeDays < 1 || options.RenewBeforeDays > 90 {
		return nil, errors.New("renew-before days must be between 1 and 90")
	}
	metadata, algorithm, err := validateCertificateMaterial(options.CertificatePEM, options.PrivateKeyPEM, domains)
	if err != nil {
		return nil, err
	}
	if options.Metadata != nil {
		metadata = options.Metadata
	}
	if !isWithin(catalog.root, record.CertificatePath) || !isWithin(catalog.root, record.PrivateKeyPath) {
		return nil, errors.New("certificate material path is outside the managed directory")
	}
	previousCertificate, err := os.ReadFile(record.CertificatePath)
	if err != nil {
		return nil, err
	}
	previousPrivateKey, err := os.ReadFile(record.PrivateKeyPath)
	if err != nil {
		return nil, err
	}
	if err := writeFileAtomic(record.CertificatePath, options.CertificatePEM, 0644); err != nil {
		return nil, err
	}
	if err := writeFileAtomic(record.PrivateKeyPath, options.PrivateKeyPEM, 0600); err != nil {
		_ = writeFileAtomic(record.CertificatePath, previousCertificate, 0644)
		return nil, err
	}
	restoreMaterial := func() {
		_ = writeFileAtomic(record.CertificatePath, previousCertificate, 0644)
		_ = writeFileAtomic(record.PrivateKeyPath, previousPrivateKey, 0600)
	}
	rollbackDeployments := func(rollbacks []DeploymentRollback) {
		for index := len(rollbacks) - 1; index >= 0; index-- {
			_ = rollbacks[index](context.Background())
		}
	}
	if err := ctx.Err(); err != nil {
		restoreMaterial()
		return nil, err
	}

	var bindings []models.CertificateBinding
	if err := catalog.db.Where("managed_certificate_id = ? AND status = ?", record.ID, BindingStatusActive).Order("created_at ASC").Find(&bindings).Error; err != nil {
		restoreMaterial()
		return nil, err
	}
	rollbacks := make([]DeploymentRollback, 0, len(bindings))
	for _, binding := range bindings {
		if catalog.deployer == nil {
			rollbackDeployments(rollbacks)
			restoreMaterial()
			return nil, errors.New("certificate deployer is not configured")
		}
		rollback, deployErr := catalog.deployer.Deploy(ctx, binding.WebsiteID, record.CertificatePath, record.PrivateKeyPath, binding.ForceHTTPS)
		if deployErr != nil {
			rollbackDeployments(rollbacks)
			restoreMaterial()
			return nil, deployErr
		}
		rollbacks = append(rollbacks, rollback)
	}

	now := time.Now().UTC()
	nextRenewAt := metadata.NotAfter.Add(-time.Duration(options.RenewBeforeDays) * 24 * time.Hour)
	updates := map[string]any{
		"email": options.Email, "directory_url": options.DirectoryURL, "challenge_type": challengeType,
		"dns_account_id": options.DNSAccountID, "challenge_website_id": options.ChallengeWebsiteID,
		"serial_number": metadata.SerialNumber.String(), "issuer": metadata.Issuer.String(), "algorithm": algorithm,
		"status": certificateStatus(metadata.NotAfter, now, options.RenewBeforeDays), "auto_renew": options.AutoRenew,
		"renew_before_days": options.RenewBeforeDays, "not_before": metadata.NotBefore.UTC(), "not_after": metadata.NotAfter.UTC(),
		"last_renew_at": now, "next_renew_at": nextRenewAt, "last_error": "", "updated_at": now,
	}
	persistErr := catalog.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.ManagedCertificate{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&models.Certificate{}).Where("managed_id = ?", record.ID).Updates(map[string]any{
			"provider": "acme", "email": options.Email, "domains": strings.Join(domains, ","), "directory_url": options.DirectoryURL,
			"challenge_type": challengeType, "dns_account_id": options.DNSAccountID,
			"certificate_path": record.CertificatePath, "private_key_path": record.PrivateKeyPath,
			"serial_number": metadata.SerialNumber.String(), "issuer": metadata.Issuer.String(),
			"status": certificateStatus(metadata.NotAfter, now, options.RenewBeforeDays), "auto_renew": options.AutoRenew,
			"renew_before_days": options.RenewBeforeDays, "not_before": metadata.NotBefore.UTC(), "not_after": metadata.NotAfter.UTC(),
			"last_renew_at": now, "next_renew_at": nextRenewAt, "last_error": "", "updated_at": now,
		}).Error
	})
	if persistErr != nil {
		rollbackDeployments(rollbacks)
		restoreMaterial()
		return nil, persistErr
	}
	updated := *record
	updated.Email = options.Email
	updated.DirectoryURL = options.DirectoryURL
	updated.ChallengeType = challengeType
	updated.DNSAccountID = options.DNSAccountID
	updated.ChallengeWebsiteID = options.ChallengeWebsiteID
	updated.SerialNumber = metadata.SerialNumber.String()
	updated.Issuer = metadata.Issuer.String()
	updated.Algorithm = algorithm
	updated.Status = certificateStatus(metadata.NotAfter, now, options.RenewBeforeDays)
	updated.AutoRenew = options.AutoRenew
	updated.RenewBeforeDays = options.RenewBeforeDays
	updated.NotBefore = metadata.NotBefore.UTC()
	updated.NotAfter = metadata.NotAfter.UTC()
	updated.LastRenewAt = &now
	updated.NextRenewAt = &nextRenewAt
	updated.LastError = ""
	updated.UpdatedAt = now
	return &updated, nil
}

// bigIntLimit is deliberately large enough for a positive serial while still
// keeping the serial bounded and compatible with common certificate tooling.
var bigIntLimit = func() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return limit
}()

func (catalog *Catalog) persistMaterial(provider string, domains []string, certificatePEM, privateKeyPEM []byte, metadata *x509.Certificate, algorithm, remark string, autoRenew bool, renewBeforeDays int) (*models.ManagedCertificate, error) {
	return catalog.persistMaterialWithACME(provider, domains, certificatePEM, privateKeyPEM, metadata, algorithm, "", "", "", "", 0, remark, autoRenew, renewBeforeDays)
}

func (catalog *Catalog) persistMaterialWithACME(provider string, domains []string, certificatePEM, privateKeyPEM []byte, metadata *x509.Certificate, algorithm, email, directoryURL, challengeType, dnsAccountID string, challengeWebsiteID int64, remark string, autoRenew bool, renewBeforeDays int) (*models.ManagedCertificate, error) {
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
		Email: email, DirectoryURL: directoryURL, ChallengeType: challengeType, DNSAccountID: dnsAccountID, ChallengeWebsiteID: challengeWebsiteID,
		CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
		SerialNumber: metadata.SerialNumber.String(), Issuer: metadata.Issuer.String(),
		Algorithm: algorithm, Status: certificateStatus(metadata.NotAfter, now, renewBeforeDays),
		AutoRenew: autoRenew, RenewBeforeDays: renewBeforeDays, NotBefore: metadata.NotBefore.UTC(),
		NotAfter: metadata.NotAfter.UTC(), Remark: truncate(remark, 512), CreatedAt: now, UpdatedAt: now,
	}
	if provider == "acme" {
		record.LastRenewAt = &now
		nextRenewAt := metadata.NotAfter.Add(-time.Duration(renewBeforeDays) * 24 * time.Hour)
		record.NextRenewAt = &nextRenewAt
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
	domains, err := certificateDomains(website.Domain)
	if err != nil {
		return nil, err
	}
	if !isWithin(catalog.root, record.CertificatePath) || !isWithin(catalog.root, record.PrivateKeyPath) {
		return nil, errors.New("certificate material path is outside the managed directory")
	}
	certificatePEM, err := os.ReadFile(record.CertificatePath)
	if err != nil {
		return nil, fmt.Errorf("read certificate material: %w", err)
	}
	privateKeyPEM, err := os.ReadFile(record.PrivateKeyPath)
	if err != nil {
		return nil, fmt.Errorf("read certificate material: %w", err)
	}
	if _, _, err := validateCertificateMaterial(certificatePEM, privateKeyPEM, domains); err != nil {
		return nil, fmt.Errorf("certificate bind validation failed: %w", err)
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
		legacy := &models.Certificate{ID: uuid.NewString(), WebsiteID: websiteID, ManagedID: record.ID, Provider: record.Provider, Email: record.Email, Domains: record.Domains, DirectoryURL: record.DirectoryURL, ChallengeType: record.ChallengeType, DNSAccountID: record.DNSAccountID, CertificatePath: record.CertificatePath, PrivateKeyPath: record.PrivateKeyPath, SerialNumber: record.SerialNumber, Issuer: record.Issuer, Status: models.CertificateStatusActive, AutoRenew: record.AutoRenew, RenewBeforeDays: record.RenewBeforeDays, ForceHTTPS: forceHTTPS, NotBefore: record.NotBefore, NotAfter: record.NotAfter, LastRenewAt: record.LastRenewAt, NextRenewAt: record.NextRenewAt, LastError: record.LastError, CreatedAt: now, UpdatedAt: now}
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
	var account models.DNSAccount
	if id != "" {
		if err := catalog.db.First(&account, "id = ?", id).Error; err != nil {
			return nil, err
		}
	}
	providerNeedsTwo := provider == "aliyun" || provider == "tencentcloud"
	providerChanged := account.ID != "" && account.Provider != provider
	if providerChanged && strings.TrimSpace(credentialOne) == "" {
		return nil, errors.New("new DNS provider credentials are required")
	}
	if strings.TrimSpace(credentialOne) == "" && (account.ID == "" || !account.CredentialConfigured) {
		return nil, errors.New("DNS account credential is required")
	}
	if providerNeedsTwo && (account.ID == "" || providerChanged || strings.TrimSpace(account.CredentialTwo) == "") && strings.TrimSpace(credentialTwo) == "" {
		return nil, errors.New("DNS provider requires a second credential")
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
	id = strings.TrimSpace(id)
	var count int64
	if err := catalog.db.Model(&models.ManagedCertificate{}).
		Where("provider = ? AND dns_account_id = ? AND status <> ?", "acme", id, models.CertificateStatusDisabled).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("DNS account is used by an active ACME certificate")
	}
	return catalog.db.Delete(&models.DNSAccount{}, "id = ?", id).Error
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

func normalizeSelfSignedOptions(options SelfSignedOptions) (SelfSignedOptions, error) {
	domains, err := normalizeManagedDomains(options.Domains)
	if err != nil {
		hasDomain := false
		for _, value := range options.Domains {
			if strings.TrimSpace(value) != "" {
				hasDomain = true
				break
			}
		}
		message := "自签证书至少需要填写一个域名"
		if !hasDomain {
			message = "自签证书域名不能为空，请至少填写一个有效域名"
		} else if len(options.Domains) > 100 {
			message = "自签证书域名不能超过 100 个"
		} else {
			message = "自签证书域名格式不正确，请检查域名是否包含空格或格式不完整"
		}
		return SelfSignedOptions{}, &RequestValidationError{Field: "domains", Message: message}
	}
	options.Domains = domains

	options.Algorithm = strings.ToLower(strings.TrimSpace(options.Algorithm))
	if options.Algorithm == "" {
		return SelfSignedOptions{}, &RequestValidationError{Field: "algorithm", Message: "请选择自签证书密钥算法"}
	}
	if !IsSupportedKeyAlgorithm(options.Algorithm) {
		return SelfSignedOptions{}, &RequestValidationError{Field: "algorithm", Message: "不支持当前自签证书密钥算法，请从算法列表中选择"}
	}

	if options.ValidityYears == 0 {
		options.ValidityYears = 10
	}
	if options.ValidityYears < 1 || options.ValidityYears > 30 {
		return SelfSignedOptions{}, &RequestValidationError{Field: "validityYears", Message: "自签证书有效期必须是 1 到 30 年之间的整数"}
	}
	if options.RenewBeforeDays == 0 {
		options.RenewBeforeDays = 30
	}
	if options.RenewBeforeDays < 1 || options.RenewBeforeDays > 90 {
		return SelfSignedOptions{}, &RequestValidationError{Field: "renewBeforeDays", Message: "续期提前天数必须是 1 到 90 之间的整数"}
	}
	return options, nil
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
		if !certificateMatchesDomain(leaf, domain) {
			return nil, "", fmt.Errorf("certificate does not cover domain %s", domain)
		}
	}
	return leaf, publicKeyAlgorithm(leaf.PublicKey), nil
}

func certificateMatchesDomain(certificate *x509.Certificate, domain string) bool {
	if certificate == nil {
		return false
	}
	domain = strings.TrimSpace(domain)
	if strings.HasPrefix(domain, "*.") {
		for _, name := range certificate.DNSNames {
			if strings.EqualFold(name, domain) {
				return true
			}
		}
		return false
	}
	return certificate.VerifyHostname(domain) == nil
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
