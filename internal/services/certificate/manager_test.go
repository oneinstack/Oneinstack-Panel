package certificate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"oneinstack/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type fakeIssuer struct {
	started chan struct{}
	block   bool
	err     error
	once    sync.Once
}

func (issuer *fakeIssuer) Issue(
	ctx context.Context,
	request IssueRequest,
	report ProgressReporter,
) (*IssuedCertificate, error) {
	if issuer.started != nil {
		issuer.once.Do(func() { close(issuer.started) })
	}
	report(50, "fake ACME validation")
	if issuer.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if issuer.err != nil {
		return nil, issuer.err
	}
	return testCertificate(request.Domains, time.Now().Add(90*24*time.Hour))
}

type fakeDeployer struct {
	mu              sync.Mutex
	ensureCalls     int
	deployCalls     int
	disableCalls    int
	rollbackCalls   int
	failDeploy      bool
	certificatePath string
	privateKeyPath  string
	forceHTTPS      bool
}

func (deployer *fakeDeployer) EnsureChallenge(context.Context, int64) error {
	deployer.mu.Lock()
	defer deployer.mu.Unlock()
	deployer.ensureCalls++
	return nil
}

func (deployer *fakeDeployer) Deploy(
	_ context.Context,
	_ int64,
	certificatePath, privateKeyPath string,
	forceHTTPS bool,
) (DeploymentRollback, error) {
	deployer.mu.Lock()
	defer deployer.mu.Unlock()
	deployer.deployCalls++
	deployer.certificatePath = certificatePath
	deployer.privateKeyPath = privateKeyPath
	deployer.forceHTTPS = forceHTTPS
	if deployer.failDeploy {
		return nil, errors.New("nginx config rejected")
	}
	return func(context.Context) error {
		deployer.mu.Lock()
		defer deployer.mu.Unlock()
		deployer.rollbackCalls++
		return nil
	}, nil
}

func (deployer *fakeDeployer) Disable(context.Context, int64) (DeploymentRollback, error) {
	deployer.mu.Lock()
	defer deployer.mu.Unlock()
	deployer.disableCalls++
	return func(context.Context) error { return nil }, nil
}

func TestManagerIssuesValidatesAndDeploysCertificate(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	deployer := &fakeDeployer{}
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, deployer)
	defer stopCertificateTestManager(t, manager)

	task, err := manager.SubmitIssue(IssueOptions{
		WebsiteID:       website.ID,
		Email:           "admin@example.com",
		AutoRenew:       true,
		RenewBeforeDays: 30,
		ForceHTTPS:      true,
		RequestedBy:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	task = waitCertificateTask(t, manager, task.ID)
	if task.Status != models.CertificateTaskStatusSucceeded || task.Progress != 100 {
		t.Fatalf("unexpected task result: %#v", task)
	}
	certificate, err := manager.GetCertificateByWebsite(website.ID)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Status != models.CertificateStatusActive ||
		!certificate.AutoRenew || !certificate.ForceHTTPS ||
		certificate.CertificatePath == "" || certificate.PrivateKeyPath == "" {
		t.Fatalf("unexpected certificate: %#v", certificate)
	}
	if info, err := os.Stat(certificate.PrivateKeyPath); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("private key permissions = %04o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(certificate.CertificatePath); err != nil {
		t.Fatal(err)
	}
	if deployer.ensureCalls != 1 || deployer.deployCalls != 1 ||
		!deployer.forceHTTPS {
		t.Fatalf("unexpected deployment calls: %#v", deployer)
	}
	logResult, err := manager.ReadTaskLog(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !logResult.EOF || logResult.Content == "" {
		t.Fatalf("unexpected task log: %#v", logResult)
	}
}

func TestManagerDoesNotPersistCertificateWhenNginxDeploymentFails(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	manager := newCertificateTestManager(
		t,
		db,
		&fakeIssuer{},
		&fakeDeployer{failDeploy: true},
	)
	defer stopCertificateTestManager(t, manager)
	task, err := manager.SubmitIssue(IssueOptions{
		WebsiteID:       website.ID,
		Email:           "admin@example.com",
		AutoRenew:       true,
		RenewBeforeDays: 30,
		RequestedBy:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	task = waitCertificateTask(t, manager, task.ID)
	if task.Status != models.CertificateTaskStatusFailed ||
		task.ErrorCode != "NGINX_DEPLOY_FAILED" {
		t.Fatalf("unexpected task result: %#v", task)
	}
	var count int64
	if err := db.Model(&models.Certificate{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("persisted certificates = %d, want 0", count)
	}
}

func TestManagerCancelsRunningCertificateTask(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	issuer := &fakeIssuer{started: make(chan struct{}), block: true}
	manager := newCertificateTestManager(t, db, issuer, &fakeDeployer{})
	defer stopCertificateTestManager(t, manager)
	task, err := manager.SubmitIssue(IssueOptions{
		WebsiteID:       website.ID,
		Email:           "admin@example.com",
		AutoRenew:       true,
		RenewBeforeDays: 30,
		RequestedBy:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-issuer.started:
	case <-time.After(3 * time.Second):
		t.Fatal("issuer did not start")
	}
	if _, err := manager.Cancel(task.ID); err != nil {
		t.Fatal(err)
	}
	task = waitCertificateTask(t, manager, task.ID)
	if task.Status != models.CertificateTaskStatusCanceled {
		t.Fatalf("task status = %s, want canceled", task.Status)
	}
}

func TestManagerDisablePublishesHTTPConfigAndStopsRenewal(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	deployer := &fakeDeployer{}
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, deployer)
	defer stopCertificateTestManager(t, manager)
	task, err := manager.SubmitIssue(IssueOptions{
		WebsiteID:       website.ID,
		Email:           "admin@example.com",
		AutoRenew:       true,
		RenewBeforeDays: 30,
		RequestedBy:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitCertificateTask(t, manager, task.ID)
	certificate, err := manager.Disable(website.ID)
	if err != nil {
		t.Fatal(err)
	}
	if certificate.Status != models.CertificateStatusDisabled ||
		certificate.AutoRenew || certificate.ForceHTTPS ||
		certificate.NextRenewAt != nil || deployer.disableCalls != 1 {
		t.Fatalf("unexpected disabled certificate: %#v, deployer=%#v", certificate, deployer)
	}
}

func TestRenewalSchedulerQueuesDueCertificateAndUpdatesExpiredStatus(t *testing.T) {
	db := openCertificateTestDB(t)
	website := createCertificateTestWebsite(t, db)
	manager := newCertificateTestManager(t, db, &fakeIssuer{}, &fakeDeployer{})
	defer stopCertificateTestManager(t, manager)
	now := time.Now().UTC()
	dueAt := now.Add(-time.Hour)
	certificate := &models.Certificate{
		ID:              "3b62ed1a-41d6-4022-9545-5677de5390ef",
		WebsiteID:       website.ID,
		Provider:        "acme",
		Email:           "admin@example.com",
		Domains:         website.Domain,
		DirectoryURL:    "https://acme.test/directory",
		CertificatePath: "/tmp/old-fullchain.pem",
		PrivateKeyPath:  "/tmp/old-privkey.pem",
		Status:          models.CertificateStatusExpiring,
		AutoRenew:       true,
		RenewBeforeDays: 30,
		NotBefore:       now.Add(-60 * 24 * time.Hour),
		NotAfter:        now.Add(10 * 24 * time.Hour),
		NextRenewAt:     &dueAt,
	}
	if err := db.Create(certificate).Error; err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewRenewalScheduler(manager, "15 3 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	var task models.CertificateTask
	if err := db.Order("created_at DESC").First(&task, "website_id = ?", website.ID).Error; err != nil {
		t.Fatal(err)
	}
	completed := waitCertificateTask(t, manager, task.ID)
	if completed.Status != models.CertificateTaskStatusSucceeded ||
		completed.Operation != models.CertificateTaskOperationRenew {
		t.Fatalf("unexpected renewal task: %#v", completed)
	}
	renewed, err := manager.GetCertificateByWebsite(website.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.NotAfter.After(now.Add(80*24*time.Hour)) || renewed.LastError != "" {
		t.Fatalf("certificate was not renewed: %#v", renewed)
	}

	expiredWebsite := &models.Website{
		Name: "expired.example.com", Domain: "expired.example.com",
		Type: "static", RootDir: "/tmp/expired.example.com",
	}
	if err := db.Create(expiredWebsite).Error; err != nil {
		t.Fatal(err)
	}
	expired := &models.Certificate{
		ID:              "74b66e56-01c8-46c4-8cff-5b42fa28e582",
		WebsiteID:       expiredWebsite.ID,
		Provider:        "acme",
		Email:           "admin@example.com",
		Domains:         expiredWebsite.Domain,
		DirectoryURL:    "https://acme.test/directory",
		CertificatePath: "/tmp/expired-fullchain.pem",
		PrivateKeyPath:  "/tmp/expired-privkey.pem",
		Status:          models.CertificateStatusActive,
		AutoRenew:       false,
		RenewBeforeDays: 30,
		NotBefore:       now.Add(-91 * 24 * time.Hour),
		NotAfter:        now.Add(-time.Hour),
	}
	if err := db.Create(expired).Error; err != nil {
		t.Fatal(err)
	}
	if err := scheduler.Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := db.First(expired, "id = ?", expired.ID).Error; err != nil {
		t.Fatal(err)
	}
	if expired.Status != models.CertificateStatusExpired {
		t.Fatalf("expired certificate status = %s", expired.Status)
	}
}

func openCertificateTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "certificate.db")))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&models.Website{},
		&models.Certificate{},
		&models.CertificateTask{},
		&models.CertificateOperationLock{},
	); err != nil {
		t.Fatal(err)
	}
	return db
}

func createCertificateTestWebsite(t *testing.T, db *gorm.DB) *models.Website {
	t.Helper()
	website := &models.Website{
		Name:    "example.com",
		Domain:  "example.com,www.example.com",
		Type:    "static",
		RootDir: "/tmp/example.com",
	}
	if err := db.Create(website).Error; err != nil {
		t.Fatal(err)
	}
	return website
}

func newCertificateTestManager(
	t *testing.T,
	db *gorm.DB,
	issuer Issuer,
	deployer Deployer,
) *Manager {
	t.Helper()
	root := t.TempDir()
	manager := NewManager(
		db,
		filepath.Join(root, "certificates"),
		filepath.Join(root, "challenges"),
		"https://acme.test/directory",
		time.Minute,
		issuer,
		deployer,
	)
	if err := manager.Start(); err != nil {
		t.Fatal(err)
	}
	return manager
}

func waitCertificateTask(t *testing.T, manager *Manager, taskID string) *models.CertificateTask {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := manager.GetTask(taskID)
		if err != nil {
			t.Fatal(err)
		}
		if models.IsCertificateTaskTerminal(task.Status) {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("certificate task did not finish")
	return nil
}

func stopCertificateTestManager(t *testing.T, manager *Manager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func testCertificate(domains []string, notAfter time.Time) (*IssuedCertificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now().Add(-time.Minute)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42),
		Subject:      pkix.Name{CommonName: domains[0]},
		Issuer:       pkix.Name{CommonName: "OneinStack Test CA"},
		NotBefore:    now,
		NotAfter:     notAfter,
		DNSNames:     domains,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		&key.PublicKey,
		key,
	)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &IssuedCertificate{
		CertificatePEM: pem.EncodeToMemory(&pem.Block{
			Type: "CERTIFICATE", Bytes: certificateDER,
		}),
		PrivateKeyPEM: pem.EncodeToMemory(&pem.Block{
			Type: "EC PRIVATE KEY", Bytes: keyDER,
		}),
	}, nil
}
