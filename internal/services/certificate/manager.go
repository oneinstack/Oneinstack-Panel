package certificate

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oneinstack/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	certificateQueueSize = 32
	maxCertificateLog    = 64 * 1024
)

type DeploymentRollback = func(context.Context) error

type Deployer interface {
	EnsureChallenge(context.Context, int64) error
	Deploy(context.Context, int64, string, string, bool) (DeploymentRollback, error)
	Disable(context.Context, int64) (DeploymentRollback, error)
}

type IssueOptions struct {
	WebsiteID       int64
	Email           string
	AutoRenew       bool
	RenewBeforeDays int
	ForceHTTPS      bool
	RequestedBy     int64
}

type ManagedIssueOptions struct {
	ChallengeType   string
	WebsiteID       int64
	Domains         []string
	Email           string
	DNSAccountID    string
	AutoRenew       bool
	RenewBeforeDays int
	Remark          string
	RequestedBy     int64
}

type TaskListOptions struct {
	WebsiteID int64
	Status    string
	Page      int
	PageSize  int
}

type TaskList struct {
	Data     []models.CertificateTask `json:"data"`
	Total    int64                    `json:"total"`
	Page     int                      `json:"page"`
	PageSize int                      `json:"pageSize"`
}

type LogResult struct {
	Content string `json:"content"`
	EOF     bool   `json:"eof"`
}

type Manager struct {
	db              *gorm.DB
	certificateRoot string
	challengeRoot   string
	directoryURL    string
	issueTimeout    time.Duration
	issuer          Issuer
	deployer        Deployer
	queue           chan string
	stopCh          chan struct{}

	startOnce  sync.Once
	startErr   error
	stopOnce   sync.Once
	submitMu   sync.Mutex
	deployerMu sync.RWMutex
	cancelMu   sync.Mutex
	cancels    map[string]context.CancelFunc
	runWG      sync.WaitGroup
	stopping   atomic.Bool
}

// TaskReader provides read-only access to certificate tasks without starting
// the worker or requiring a Web server deployer. Task history must remain
// inspectable when deployment dependencies are unavailable.
type TaskReader struct {
	db *gorm.DB
}

func NewTaskReader(db *gorm.DB) *TaskReader {
	return &TaskReader{db: db}
}

func (reader *TaskReader) configured() error {
	if reader == nil || reader.db == nil {
		return errors.New("certificate task database is not initialized")
	}
	return nil
}

func (reader *TaskReader) GetTask(taskID string) (*models.CertificateTask, error) {
	if err := reader.configured(); err != nil {
		return nil, err
	}
	return getCertificateTask(reader.db, taskID)
}

func (reader *TaskReader) ListTasks(options TaskListOptions) (*TaskList, error) {
	if err := reader.configured(); err != nil {
		return nil, err
	}
	return listCertificateTasks(reader.db, options)
}

func (reader *TaskReader) ReadTaskLog(taskID string) (*LogResult, error) {
	if err := reader.configured(); err != nil {
		return nil, err
	}
	return readCertificateTaskLog(reader.db, taskID)
}

func NewManager(
	db *gorm.DB,
	certificateRoot, challengeRoot, directoryURL string,
	issueTimeout time.Duration,
	issuer Issuer,
	deployer Deployer,
) *Manager {
	return &Manager{
		db:              db,
		certificateRoot: filepath.Clean(certificateRoot),
		challengeRoot:   filepath.Clean(challengeRoot),
		directoryURL:    strings.TrimSpace(directoryURL),
		issueTimeout:    issueTimeout,
		issuer:          issuer,
		deployer:        deployer,
		queue:           make(chan string, certificateQueueSize),
		stopCh:          make(chan struct{}),
		cancels:         make(map[string]context.CancelFunc),
	}
}

// SetDeployer refreshes the runtime deployment adapter without restarting the
// worker. Web server switching is independent from the certificate task
// queue, so recreating the manager here would incorrectly interrupt queued
// tasks just to pick up the new active Web server.
func (manager *Manager) SetDeployer(deployer Deployer) {
	if manager == nil {
		return
	}
	manager.deployerMu.Lock()
	manager.deployer = deployer
	manager.deployerMu.Unlock()
}

func (manager *Manager) currentDeployer() Deployer {
	if manager == nil {
		return nil
	}
	manager.deployerMu.RLock()
	deployer := manager.deployer
	manager.deployerMu.RUnlock()
	return deployer
}

func (manager *Manager) Start() error {
	manager.startOnce.Do(func() {
		switch {
		case manager.db == nil:
			manager.startErr = errors.New("certificate database is not initialized")
		case manager.issuer == nil:
			manager.startErr = errors.New("certificate issuer is not configured")
		case invalidPathRoot(manager.certificateRoot):
			manager.startErr = errors.New("certificate directory is invalid")
		case manager.directoryURL == "":
			manager.startErr = errors.New("ACME directory URL is required")
		case manager.issueTimeout < time.Minute || manager.issueTimeout > 2*time.Hour:
			manager.startErr = errors.New("ACME issue timeout must be between 1 and 120 minutes")
		}
		if manager.startErr != nil {
			return
		}
		if err := os.MkdirAll(manager.certificateRoot, 0700); err != nil {
			manager.startErr = fmt.Errorf("create certificate directory: %w", err)
			return
		}
		if err := os.Chmod(manager.certificateRoot, 0700); err != nil {
			manager.startErr = fmt.Errorf("secure certificate directory: %w", err)
			return
		}
		now := time.Now().UTC()
		if err := manager.db.Model(&models.CertificateTask{}).
			Where("status IN ?", models.ActiveCertificateTaskStatuses()).
			Updates(map[string]any{
				"status":        models.CertificateTaskStatusInterrupted,
				"error_code":    "PANEL_RESTARTED",
				"error_message": "Panel 重启，证书任务已中断",
				"message":       "任务已中断",
				"finished_at":   now,
			}).Error; err != nil {
			manager.startErr = fmt.Errorf("reconcile certificate tasks: %w", err)
			return
		}
		if err := manager.db.Session(&gorm.Session{AllowGlobalUpdate: true}).
			Delete(&models.CertificateOperationLock{}).Error; err != nil {
			manager.startErr = fmt.Errorf("clear certificate operation locks: %w", err)
			return
		}
		manager.runWG.Add(1)
		go manager.worker()
	})
	return manager.startErr
}

func invalidPathRoot(path string) bool {
	return path == "" || path == "." || path == string(filepath.Separator) || !filepath.IsAbs(path)
}

func (manager *Manager) SubmitIssue(options IssueOptions) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	if options.WebsiteID <= 0 {
		return nil, errors.New("website is required")
	}
	email, err := normalizeEmail(options.Email)
	if err != nil {
		return nil, err
	}
	if options.RenewBeforeDays == 0 {
		options.RenewBeforeDays = 30
	}
	if options.RenewBeforeDays < 1 || options.RenewBeforeDays > 90 {
		return nil, errors.New("renew-before days must be between 1 and 90")
	}
	var website models.Website
	if err := manager.db.First(&website, "id = ?", options.WebsiteID).Error; err != nil {
		return nil, err
	}
	domains, err := certificateDomains(website.Domain)
	if err != nil {
		return nil, err
	}
	return manager.submit(&models.CertificateTask{
		Operation:       models.CertificateTaskOperationIssue,
		WebsiteID:       website.ID,
		WebsiteName:     website.Name,
		Email:           email,
		Domains:         strings.Join(domains, ","),
		DirectoryURL:    manager.directoryURL,
		AutoRenew:       options.AutoRenew,
		RenewBeforeDays: options.RenewBeforeDays,
		ForceHTTPS:      options.ForceHTTPS,
		RequestedBy:     options.RequestedBy,
	})
}

func (manager *Manager) SubmitManagedIssue(options ManagedIssueOptions) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	options, websiteID, websiteName, domains, err := manager.prepareManagedIssue(options)
	if err != nil {
		return nil, err
	}
	return manager.submit(&models.CertificateTask{
		Operation:       models.CertificateTaskOperationManagedIssue,
		WebsiteID:       websiteID,
		WebsiteName:     websiteName,
		Email:           options.Email,
		Domains:         strings.Join(domains, ","),
		DirectoryURL:    manager.directoryURL,
		ChallengeType:   options.ChallengeType,
		DNSAccountID:    options.DNSAccountID,
		AutoRenew:       options.AutoRenew,
		RenewBeforeDays: options.RenewBeforeDays,
		Remark:          options.Remark,
		RequestedBy:     options.RequestedBy,
	})
}

func (manager *Manager) ValidateManagedIssue(options ManagedIssueOptions) error {
	_, _, _, _, err := manager.prepareManagedIssue(options)
	return err
}

func (manager *Manager) prepareManagedIssue(options ManagedIssueOptions) (ManagedIssueOptions, int64, string, []string, error) {
	if manager == nil || manager.db == nil {
		return ManagedIssueOptions{}, 0, "", nil, errors.New("certificate database is not initialized")
	}
	challengeType, err := normalizeChallengeType(options.ChallengeType)
	if err != nil {
		return ManagedIssueOptions{}, 0, "", nil, err
	}
	email, err := normalizeEmail(options.Email)
	if err != nil {
		return ManagedIssueOptions{}, 0, "", nil, err
	}
	if options.RenewBeforeDays == 0 {
		options.RenewBeforeDays = 30
	}
	if options.RenewBeforeDays < 1 || options.RenewBeforeDays > 90 {
		return ManagedIssueOptions{}, 0, "", nil, errors.New("renew-before days must be between 1 and 90")
	}
	options.ChallengeType = challengeType
	options.Email = email
	options.DNSAccountID = strings.TrimSpace(options.DNSAccountID)

	websiteName := "certificate"
	websiteID := int64(0)
	var domains []string
	if challengeType == ChallengeHTTP01 {
		if options.WebsiteID <= 0 {
			return ManagedIssueOptions{}, 0, "", nil, errors.New("website is required for HTTP-01 issuance")
		}
		var website models.Website
		if err := manager.db.First(&website, "id = ?", options.WebsiteID).Error; err != nil {
			return ManagedIssueOptions{}, 0, "", nil, err
		}
		websiteID = website.ID
		websiteName = website.Name
		domains, err = certificateDomains(website.Domain)
		if err != nil {
			return ManagedIssueOptions{}, 0, "", nil, err
		}
		if len(options.Domains) > 0 {
			requestedDomains, domainErr := normalizeACMEDomains(options.Domains, challengeType)
			if domainErr != nil {
				return ManagedIssueOptions{}, 0, "", nil, domainErr
			}
			if !sameDomains(domains, requestedDomains) {
				return ManagedIssueOptions{}, 0, "", nil, errors.New("HTTP-01 domains must match the selected website")
			}
		}
	} else {
		if strings.TrimSpace(options.DNSAccountID) == "" {
			return ManagedIssueOptions{}, 0, "", nil, errors.New("DNS account is required for DNS-01 issuance")
		}
		var account models.DNSAccount
		if err := manager.db.First(&account, "id = ?", options.DNSAccountID).Error; err != nil {
			return ManagedIssueOptions{}, 0, "", nil, err
		}
		if !account.Enabled || !account.CredentialConfigured {
			return ManagedIssueOptions{}, 0, "", nil, errors.New("DNS account is disabled or not configured")
		}
		domains, err = normalizeACMEDomains(options.Domains, challengeType)
		if err != nil {
			return ManagedIssueOptions{}, 0, "", nil, err
		}
	}
	return options, websiteID, websiteName, domains, nil
}

func (manager *Manager) SubmitRenew(certificateID string, requestedBy int64) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	var certificate models.Certificate
	if err := manager.db.First(&certificate, "id = ?", strings.TrimSpace(certificateID)).Error; err != nil {
		return nil, err
	}
	if certificate.Status == models.CertificateStatusDisabled {
		return nil, errors.New("disabled certificate cannot be renewed")
	}
	var website models.Website
	if err := manager.db.First(&website, "id = ?", certificate.WebsiteID).Error; err != nil {
		return nil, err
	}
	currentDomains, err := certificateDomains(website.Domain)
	if err != nil {
		return nil, err
	}
	if !sameDomains(strings.Split(certificate.Domains, ","), currentDomains) {
		return nil, errors.New("website domains changed; request a new certificate instead of renewing")
	}
	directoryURL := strings.TrimSpace(certificate.DirectoryURL)
	if directoryURL == "" {
		directoryURL = manager.directoryURL
	}
	return manager.submit(&models.CertificateTask{
		Operation:       models.CertificateTaskOperationRenew,
		WebsiteID:       website.ID,
		WebsiteName:     website.Name,
		CertificateID:   certificate.ID,
		Email:           certificate.Email,
		Domains:         strings.Join(currentDomains, ","),
		DirectoryURL:    directoryURL,
		ChallengeType:   defaultChallengeType(certificate.ChallengeType),
		DNSAccountID:    certificate.DNSAccountID,
		AutoRenew:       certificate.AutoRenew,
		RenewBeforeDays: certificate.RenewBeforeDays,
		ForceHTTPS:      certificate.ForceHTTPS,
		RequestedBy:     requestedBy,
	})
}

func (manager *Manager) SubmitManagedRenew(managedID string, requestedBy int64) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	var certificate models.ManagedCertificate
	if err := manager.db.First(&certificate, "id = ?", strings.TrimSpace(managedID)).Error; err != nil {
		return nil, err
	}
	if certificate.Provider != "acme" {
		return nil, errors.New("only ACME certificates can be renewed")
	}
	challengeType, err := normalizeChallengeType(certificate.ChallengeType)
	if err != nil {
		return nil, err
	}
	domains, err := normalizeACMEDomains(taskDomains(certificate.Domains), challengeType)
	if err != nil {
		return nil, err
	}
	email, err := normalizeEmail(certificate.Email)
	if err != nil {
		return nil, err
	}
	if certificate.RenewBeforeDays == 0 {
		certificate.RenewBeforeDays = 30
	}
	if certificate.RenewBeforeDays < 1 || certificate.RenewBeforeDays > 90 {
		return nil, errors.New("renew-before days must be between 1 and 90")
	}
	websiteID := certificate.ChallengeWebsiteID
	websiteName := "certificate"
	if challengeType == ChallengeHTTP01 {
		if websiteID <= 0 {
			return nil, errors.New("HTTP-01 renewal website is unavailable")
		}
		var website models.Website
		if err := manager.db.First(&website, "id = ?", websiteID).Error; err != nil {
			return nil, err
		}
		websiteName = website.Name
		currentDomains, domainErr := certificateDomains(website.Domain)
		if domainErr != nil {
			return nil, domainErr
		}
		if !sameDomains(domains, currentDomains) {
			return nil, errors.New("website domains changed; request a new certificate instead of renewing")
		}
	} else {
		if _, _, err := loadDNSChallengeProvider(manager.db, certificate.DNSAccountID); err != nil {
			return nil, err
		}
	}
	directoryURL := strings.TrimSpace(certificate.DirectoryURL)
	if directoryURL == "" {
		directoryURL = manager.directoryURL
	}
	return manager.submit(&models.CertificateTask{
		Operation:       models.CertificateTaskOperationManagedRenew,
		WebsiteID:       websiteID,
		WebsiteName:     websiteName,
		CertificateID:   certificate.ID,
		ManagedID:       certificate.ID,
		Email:           email,
		Domains:         strings.Join(domains, ","),
		DirectoryURL:    directoryURL,
		ChallengeType:   challengeType,
		DNSAccountID:    certificate.DNSAccountID,
		AutoRenew:       certificate.AutoRenew,
		RenewBeforeDays: certificate.RenewBeforeDays,
		Remark:          certificate.Remark,
		RequestedBy:     requestedBy,
	})
}

func (manager *Manager) SubmitManagedUpload(options CreateCertificateOptions, requestedBy int64) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	if len(options.CertificatePEM) == 0 || len(options.PrivateKeyPEM) == 0 {
		return nil, errors.New("certificate and private key are required")
	}
	pending := filepath.Join(manager.certificateRoot, "pending", uuid.NewString())
	if err := os.MkdirAll(pending, 0700); err != nil {
		return nil, fmt.Errorf("create certificate task workspace: %w", err)
	}
	certPath := filepath.Join(pending, "certificate.pem")
	keyPath := filepath.Join(pending, "private-key.pem")
	if err := writeFileAtomic(certPath, options.CertificatePEM, 0600); err != nil {
		_ = os.RemoveAll(pending)
		return nil, err
	}
	if err := writeFileAtomic(keyPath, options.PrivateKeyPEM, 0600); err != nil {
		_ = os.RemoveAll(pending)
		return nil, err
	}
	task, err := manager.submit(&models.CertificateTask{
		Operation: models.CertificateTaskOperationUpload, WebsiteName: "certificate", Domains: strings.Join(options.Domains, ","),
		AutoRenew: options.AutoRenew, RenewBeforeDays: options.RenewBeforeDays, RequestedBy: requestedBy,
		InputCertPath: certPath, InputKeyPath: keyPath, Remark: options.Remark,
	})
	if err != nil {
		_ = os.RemoveAll(pending)
	}
	return task, err
}

func (manager *Manager) SubmitManagedSelfSigned(options SelfSignedOptions, requestedBy int64) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	options, err := normalizeSelfSignedOptions(options)
	if err != nil {
		return nil, err
	}
	task, err := manager.submit(&models.CertificateTask{
		Operation: models.CertificateTaskOperationSelfSigned, WebsiteName: "certificate", Domains: strings.Join(options.Domains, ","),
		AutoRenew: options.AutoRenew, RenewBeforeDays: options.RenewBeforeDays, RequestedBy: requestedBy,
		Algorithm: options.Algorithm, ValidityYears: options.ValidityYears, Remark: options.Remark,
	})
	return task, err
}

func (manager *Manager) SubmitManagedBind(managedID string, websiteID int64, forceHTTPS bool, requestedBy int64) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(managedID) == "" || websiteID <= 0 {
		return nil, errors.New("certificate and website are required")
	}
	var certificate models.ManagedCertificate
	if err := manager.db.First(&certificate, "id = ?", strings.TrimSpace(managedID)).Error; err != nil {
		return nil, err
	}
	var website models.Website
	if err := manager.db.First(&website, "id = ?", websiteID).Error; err != nil {
		return nil, err
	}
	return manager.submit(&models.CertificateTask{
		Operation: models.CertificateTaskOperationBind, WebsiteID: website.ID, WebsiteName: website.Name,
		ManagedID: certificate.ID, CertificateID: certificate.ID, Domains: certificate.Domains,
		ForceHTTPS: forceHTTPS, RequestedBy: requestedBy,
	})
}

func (manager *Manager) submit(task *models.CertificateTask) (*models.CertificateTask, error) {
	if manager.stopping.Load() {
		return nil, errors.New("certificate task manager is stopping")
	}
	manager.submitMu.Lock()
	defer manager.submitMu.Unlock()
	var active int64
	if err := manager.db.Model(&models.CertificateTask{}).
		Where("website_id = ? AND status IN ?", task.WebsiteID, models.ActiveCertificateTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, fmt.Errorf("website %s already has an active certificate task", task.WebsiteName)
	}
	now := time.Now().UTC()
	task.ID = uuid.NewString()
	task.Status = models.CertificateTaskStatusQueued
	task.Progress = 0
	task.Message = "证书任务已进入队列"
	task.CreatedAt = now
	task.UpdatedAt = now
	task.LogText = fmt.Sprintf("[%s] certificate %s task queued for %s\n",
		now.Format(time.RFC3339), task.Operation, task.WebsiteName)
	if err := manager.db.Create(task).Error; err != nil {
		return nil, fmt.Errorf("create certificate task: %w", err)
	}
	select {
	case manager.queue <- task.ID:
		return task, nil
	case <-manager.stopCh:
		manager.cleanupTaskInput(task)
		_ = manager.finish(task.ID, models.CertificateTaskStatusInterrupted, "MANAGER_STOPPED", "证书任务服务已停止")
		return nil, errors.New("certificate task manager is stopping")
	default:
		manager.cleanupTaskInput(task)
		_ = manager.finish(task.ID, models.CertificateTaskStatusFailed, "QUEUE_FULL", "证书任务队列已满")
		return nil, errors.New("certificate task queue is full")
	}
}

func (manager *Manager) worker() {
	defer manager.runWG.Done()
	for {
		select {
		case <-manager.stopCh:
			return
		case taskID := <-manager.queue:
			if manager.stopping.Load() {
				return
			}
			manager.run(taskID)
		}
	}
}

func (manager *Manager) run(taskID string) {
	var task models.CertificateTask
	if err := manager.db.First(&task, "id = ?", taskID).Error; err != nil ||
		models.IsCertificateTaskTerminal(task.Status) {
		return
	}
	lock := &models.CertificateOperationLock{
		WebsiteID:  task.WebsiteID,
		TaskID:     task.ID,
		AcquiredAt: time.Now().UTC(),
	}
	if err := manager.db.Create(lock).Error; err != nil {
		_ = manager.finish(task.ID, models.CertificateTaskStatusFailed, "WEBSITE_BUSY", "网站证书操作正在执行")
		return
	}
	defer manager.db.Delete(&models.CertificateOperationLock{}, "website_id = ? AND task_id = ?", task.WebsiteID, task.ID)

	ctx, cancel := context.WithTimeout(context.Background(), manager.issueTimeout)
	manager.cancelMu.Lock()
	manager.cancels[task.ID] = cancel
	manager.cancelMu.Unlock()
	defer func() {
		cancel()
		manager.cancelMu.Lock()
		delete(manager.cancels, task.ID)
		manager.cancelMu.Unlock()
	}()
	if task.CancelRequested || manager.stopping.Load() {
		cancel()
	}
	now := time.Now().UTC()
	_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{
		"status":     models.CertificateTaskStatusRunning,
		"progress":   5,
		"message":    "证书任务开始执行",
		"started_at": now,
	}).Error
	manager.appendLog(task.ID, "证书任务开始执行")

	report := func(progress int, message string) {
		if progress < 1 {
			progress = 1
		}
		if progress > 95 {
			progress = 95
		}
		_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{
			"progress": progress,
			"message":  truncate(message, 512),
		}).Error
		manager.appendLog(task.ID, message)
	}
	if task.Operation == models.CertificateTaskOperationUpload ||
		task.Operation == models.CertificateTaskOperationSelfSigned ||
		task.Operation == models.CertificateTaskOperationBind {
		manager.runManagedTask(ctx, &task, report)
		return
	}
	if task.Operation == models.CertificateTaskOperationManagedIssue ||
		task.Operation == models.CertificateTaskOperationManagedRenew {
		manager.runManagedACMETask(ctx, &task, report)
		return
	}
	issued, metadata, err := manager.issueACME(ctx, &task, report)
	if err != nil {
		manager.failTask(&task, acmeTaskErrorCode(&task, err), err)
		return
	}
	if err := ctx.Err(); err != nil {
		manager.failTask(&task, "TASK_CANCELED", err)
		return
	}
	report(92, "正在安全写入证书文件")
	versionID := uuid.NewString()
	versionDirectory := filepath.Join(
		manager.certificateRoot,
		"sites",
		strconv.FormatInt(task.WebsiteID, 10),
		versionID,
	)
	certificatePath := filepath.Join(versionDirectory, "fullchain.pem")
	privateKeyPath := filepath.Join(versionDirectory, "privkey.pem")
	if err := writeFileAtomic(certificatePath, issued.CertificatePEM, 0644); err != nil {
		manager.failTask(&task, "CERTIFICATE_WRITE_FAILED", err)
		return
	}
	if err := writeFileAtomic(privateKeyPath, issued.PrivateKeyPEM, 0600); err != nil {
		_ = os.RemoveAll(versionDirectory)
		manager.failTask(&task, "PRIVATE_KEY_WRITE_FAILED", err)
		return
	}
	if err := ctx.Err(); err != nil {
		_ = os.RemoveAll(versionDirectory)
		manager.failTask(&task, "TASK_CANCELED", err)
		return
	}
	deployer := manager.currentDeployer()
	if deployer == nil {
		_ = os.RemoveAll(versionDirectory)
		manager.failTask(&task, "CERTIFICATE_DEPLOYER_UNAVAILABLE", errors.New("certificate deployer is not configured"))
		return
	}
	report(96, "正在验证并重新加载 Nginx")
	rollback, err := deployer.Deploy(
		ctx,
		task.WebsiteID,
		certificatePath,
		privateKeyPath,
		task.ForceHTTPS,
	)
	if err != nil {
		_ = os.RemoveAll(versionDirectory)
		manager.failTask(&task, "NGINX_DEPLOY_FAILED", err)
		return
	}
	if err := ctx.Err(); err != nil {
		_ = rollback(context.Background())
		_ = os.RemoveAll(versionDirectory)
		manager.failTask(&task, "TASK_CANCELED", err)
		return
	}

	completedAt := time.Now().UTC()
	nextRenewAt := metadata.NotAfter.Add(
		-time.Duration(task.RenewBeforeDays) * 24 * time.Hour,
	)
	certificateID := task.CertificateID
	previousCertificatePath := ""
	previousPrivateKeyPath := ""
	if certificateID == "" {
		var existing models.Certificate
		if err := manager.db.First(&existing, "website_id = ?", task.WebsiteID).Error; err == nil {
			certificateID = existing.ID
			previousCertificatePath = existing.CertificatePath
			previousPrivateKeyPath = existing.PrivateKeyPath
		}
	} else {
		var existing models.Certificate
		if err := manager.db.First(&existing, "id = ?", certificateID).Error; err == nil {
			previousCertificatePath = existing.CertificatePath
			previousPrivateKeyPath = existing.PrivateKeyPath
		}
	}
	if certificateID == "" {
		certificateID = uuid.NewString()
	}
	certificateRecord := &models.Certificate{
		ID:              certificateID,
		WebsiteID:       task.WebsiteID,
		ManagedID:       certificateID,
		Provider:        "acme",
		Email:           task.Email,
		Domains:         task.Domains,
		DirectoryURL:    task.DirectoryURL,
		ChallengeType:   defaultChallengeType(task.ChallengeType),
		DNSAccountID:    task.DNSAccountID,
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
		SerialNumber:    metadata.SerialNumber.String(),
		Issuer:          metadata.Issuer.String(),
		Status:          certificateStatus(metadata.NotAfter, completedAt, task.RenewBeforeDays),
		AutoRenew:       task.AutoRenew,
		RenewBeforeDays: task.RenewBeforeDays,
		ForceHTTPS:      task.ForceHTTPS,
		NotBefore:       metadata.NotBefore.UTC(),
		NotAfter:        metadata.NotAfter.UTC(),
		LastRenewAt:     &completedAt,
		NextRenewAt:     &nextRenewAt,
		LastError:       "",
		CreatedAt:       completedAt,
		UpdatedAt:       completedAt,
	}
	persistErr := manager.db.Transaction(func(tx *gorm.DB) error {
		var existing models.Certificate
		err := tx.First(&existing, "website_id = ?", task.WebsiteID).Error
		if err == nil {
			certificateRecord.ID = existing.ID
			certificateRecord.ManagedID = existing.ManagedID
			if certificateRecord.ManagedID == "" {
				certificateRecord.ManagedID = existing.ID
			}
			certificateRecord.CreatedAt = existing.CreatedAt
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		} else if err := tx.Create(certificateRecord).Error; err != nil {
			return err
		}
		if err := tx.Save(certificateRecord).Error; err != nil {
			return err
		}
		return tx.Save(&models.ManagedCertificate{
			ID: certificateRecord.ManagedID, Provider: certificateRecord.Provider, Domains: certificateRecord.Domains,
			Email: certificateRecord.Email, DirectoryURL: certificateRecord.DirectoryURL, ChallengeType: certificateRecord.ChallengeType,
			DNSAccountID: certificateRecord.DNSAccountID, ChallengeWebsiteID: task.WebsiteID,
			CertificatePath: certificateRecord.CertificatePath, PrivateKeyPath: certificateRecord.PrivateKeyPath,
			SerialNumber: certificateRecord.SerialNumber, Issuer: certificateRecord.Issuer,
			Algorithm: publicKeyAlgorithm(metadata.PublicKey), Status: certificateRecord.Status,
			AutoRenew: certificateRecord.AutoRenew, RenewBeforeDays: certificateRecord.RenewBeforeDays,
			NotBefore: certificateRecord.NotBefore, NotAfter: certificateRecord.NotAfter,
			LastRenewAt: certificateRecord.LastRenewAt, NextRenewAt: certificateRecord.NextRenewAt, LastError: certificateRecord.LastError,
			CreatedAt: certificateRecord.CreatedAt, UpdatedAt: certificateRecord.UpdatedAt,
		}).Error
	})
	if persistErr != nil {
		rollbackErr := rollback(context.Background())
		_ = os.RemoveAll(versionDirectory)
		if rollbackErr != nil {
			persistErr = errors.Join(persistErr, rollbackErr)
		}
		manager.failTask(&task, "CERTIFICATE_PERSIST_FAILED", persistErr)
		return
	}
	_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).
		Update("certificate_id", certificateRecord.ID).Error
	if err := manager.removeSupersededVersion(
		previousCertificatePath,
		previousPrivateKeyPath,
		certificatePath,
	); err != nil {
		manager.appendLog(task.ID, "旧证书版本清理失败："+err.Error())
	}
	manager.appendLog(task.ID, "证书已部署，Nginx 配置验证和重载成功")
	_ = manager.finish(task.ID, models.CertificateTaskStatusSucceeded, "", "证书签发和部署成功")
}

func (manager *Manager) issueACME(ctx context.Context, task *models.CertificateTask, report ProgressReporter) (*IssuedCertificate, *x509.Certificate, error) {
	challengeType := defaultChallengeType(task.ChallengeType)
	deployer := manager.currentDeployer()
	var dnsProvider DNSChallengeProvider
	if challengeType == ChallengeHTTP01 {
		if deployer == nil {
			return nil, nil, errors.New("certificate deployer is not configured")
		}
		if err := deployer.EnsureChallenge(ctx, task.WebsiteID); err != nil {
			return nil, nil, fmt.Errorf("publish HTTP-01 route: %w", err)
		}
	} else {
		var err error
		dnsProvider, _, err = loadDNSChallengeProvider(manager.db, task.DNSAccountID)
		if err != nil {
			return nil, nil, err
		}
	}
	directoryHash := sha256.Sum256([]byte(task.DirectoryURL))
	issued, err := manager.issuer.Issue(ctx, IssueRequest{
		DirectoryURL:   task.DirectoryURL,
		Email:          task.Email,
		Domains:        strings.Split(task.Domains, ","),
		AccountKeyPath: filepath.Join(manager.certificateRoot, "accounts", hex.EncodeToString(directoryHash[:16])+".key"),
		ChallengeRoot:  manager.challengeRoot,
		ChallengeType:  challengeType,
		DNSProvider:    dnsProvider,
	}, report)
	if err != nil {
		return nil, nil, err
	}
	metadata, err := validateIssuedCertificate(issued, strings.Split(task.Domains, ","))
	if err != nil {
		return nil, nil, err
	}
	return issued, metadata, nil
}

func (manager *Manager) runManagedACMETask(ctx context.Context, task *models.CertificateTask, report ProgressReporter) {
	issued, metadata, err := manager.issueACME(ctx, task, report)
	if err != nil {
		manager.failTask(task, acmeTaskErrorCode(task, err), err)
		return
	}
	if err := ctx.Err(); err != nil {
		manager.failTask(task, "TASK_CANCELED", err)
		return
	}
	report(92, "正在安全写入证书文件")
	catalog := NewCatalog(manager.db, manager.certificateRoot, manager.currentDeployer())
	if task.Operation == models.CertificateTaskOperationManagedIssue {
		record, createErr := catalog.CreateACME(ACMECertificateOptions{
			Domains:            taskDomains(task.Domains),
			CertificatePEM:     issued.CertificatePEM,
			PrivateKeyPEM:      issued.PrivateKeyPEM,
			Email:              task.Email,
			DirectoryURL:       task.DirectoryURL,
			ChallengeType:      defaultChallengeType(task.ChallengeType),
			DNSAccountID:       task.DNSAccountID,
			ChallengeWebsiteID: task.WebsiteID,
			Metadata:           metadata,
			AutoRenew:          task.AutoRenew,
			RenewBeforeDays:    task.RenewBeforeDays,
			Remark:             task.Remark,
		})
		if createErr != nil {
			manager.failTask(task, "CERTIFICATE_PERSIST_FAILED", createErr)
			return
		}
		task.ManagedID = record.ID
		task.CertificateID = record.ID
		_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{
			"managed_id": task.ManagedID, "certificate_id": task.CertificateID,
		}).Error
	} else {
		report(96, "正在验证并重新加载已绑定网站")
		if _, renewErr := catalog.RenewACME(ctx, task.ManagedID, ACMERenewalOptions{
			CertificatePEM:     issued.CertificatePEM,
			PrivateKeyPEM:      issued.PrivateKeyPEM,
			Metadata:           metadata,
			Email:              task.Email,
			DirectoryURL:       task.DirectoryURL,
			ChallengeType:      defaultChallengeType(task.ChallengeType),
			DNSAccountID:       task.DNSAccountID,
			ChallengeWebsiteID: task.WebsiteID,
			AutoRenew:          task.AutoRenew,
			RenewBeforeDays:    task.RenewBeforeDays,
		}); renewErr != nil {
			manager.failTask(task, "CERTIFICATE_DEPLOY_FAILED", renewErr)
			return
		}
	}
	manager.appendLog(task.ID, "ACME 证书资源操作完成")
	message := "证书申请成功，已生成证书资源"
	if task.Operation == models.CertificateTaskOperationManagedRenew {
		message = "证书续期成功，已更新证书资源及绑定网站"
	}
	_ = manager.finish(task.ID, models.CertificateTaskStatusSucceeded, "", message)
}

func acmeTaskErrorCode(task *models.CertificateTask, err error) string {
	if err == nil {
		return "ACME_ISSUE_FAILED"
	}
	lower := strings.ToLower(err.Error())
	if task != nil && defaultChallengeType(task.ChallengeType) == ChallengeDNS01 {
		if strings.Contains(lower, "dns account") || strings.Contains(lower, "dns provider") {
			return "DNS_ACCOUNT_INVALID"
		}
		return "DNS_CHALLENGE_FAILED"
	}
	if task != nil && defaultChallengeType(task.ChallengeType) == ChallengeHTTP01 {
		switch {
		case isDNSLookupNotFoundError(lower):
			return "HTTP01_DNS_NOT_FOUND"
		case certificateHTTPResponseStatus(err.Error()) == 403:
			return "HTTP01_CHALLENGE_FORBIDDEN"
		case certificateHTTPResponseStatus(err.Error()) == 404:
			return "HTTP01_CHALLENGE_NOT_FOUND"
		case strings.Contains(lower, "http-01 dns lookup"):
			return "HTTP01_DNS_LOOKUP_FAILED"
		case strings.Contains(lower, "http-01 challenge"):
			return "HTTP01_CHALLENGE_FAILED"
		}
	}
	if strings.Contains(lower, "deployer") || strings.Contains(lower, "http-01 route") {
		return "CHALLENGE_CONFIG_FAILED"
	}
	return "ACME_ISSUE_FAILED"
}

func (manager *Manager) runManagedTask(ctx context.Context, task *models.CertificateTask, report ProgressReporter) {
	catalog := NewCatalog(manager.db, manager.certificateRoot, manager.currentDeployer())
	var (
		record *models.ManagedCertificate
		err    error
	)
	switch task.Operation {
	case models.CertificateTaskOperationUpload:
		report(25, "正在校验证书和私钥")
		certificatePEM, certErr := os.ReadFile(task.InputCertPath)
		if certErr != nil {
			err = certErr
			break
		}
		privateKeyPEM, keyErr := os.ReadFile(task.InputKeyPath)
		if keyErr != nil {
			err = keyErr
			break
		}
		record, err = catalog.CreateUpload(CreateCertificateOptions{
			Domains: taskDomains(task.Domains), CertificatePEM: certificatePEM, PrivateKeyPEM: privateKeyPEM,
			Remark: task.Remark, AutoRenew: task.AutoRenew, RenewBeforeDays: task.RenewBeforeDays,
		})
	case models.CertificateTaskOperationSelfSigned:
		report(30, "正在生成自签证书")
		record, err = catalog.CreateSelfSigned(SelfSignedOptions{
			Domains: taskDomains(task.Domains), Algorithm: task.Algorithm, ValidityYears: task.ValidityYears,
			Remark: task.Remark, AutoRenew: task.AutoRenew, RenewBeforeDays: task.RenewBeforeDays,
		})
	case models.CertificateTaskOperationBind:
		report(30, "正在校验证书域名覆盖范围")
		_, err = catalog.Bind(ctx, task.ManagedID, task.WebsiteID, task.ForceHTTPS)
	default:
		err = errors.New("unsupported managed certificate task")
	}
	if task.InputCertPath != "" {
		_ = os.RemoveAll(filepath.Dir(task.InputCertPath))
		_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{"input_cert_path": "", "input_key_path": ""}).Error
	}
	if err != nil {
		manager.failTask(task, "CERTIFICATE_RESOURCE_FAILED", err)
		return
	}
	if record != nil {
		task.ManagedID = record.ID
		task.CertificateID = record.ID
		_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{"managed_id": record.ID, "certificate_id": record.ID}).Error
	}
	manager.appendLog(task.ID, "证书资源操作完成")
	_ = manager.finish(task.ID, models.CertificateTaskStatusSucceeded, "", "证书资源操作成功")
}

func taskDomains(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func (manager *Manager) failTask(task *models.CertificateTask, code string, err error) {
	status := models.CertificateTaskStatusFailed
	message := truncate(err.Error(), 1024)
	if isManagedCertificateTask(task.Operation) {
		message = SafeCertificateErrorDetail(err)
	}
	if errors.Is(err, context.Canceled) || task.CancelRequested {
		if manager.stopping.Load() {
			status = models.CertificateTaskStatusInterrupted
			code = "PANEL_STOPPED"
			message = "Panel 停止，证书任务已中断"
		} else {
			status = models.CertificateTaskStatusCanceled
			code = "TASK_CANCELED"
			message = "证书任务已取消"
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "ACME_TIMEOUT"
		message = "证书签发超时"
	} else if task.Operation == models.CertificateTaskOperationSelfSigned {
		message = SafeCertificateErrorDetail(err)
	}
	manager.appendLog(task.ID, "任务失败："+message)
	_ = manager.finish(task.ID, status, code, message)
	if task.CertificateID != "" {
		retryAt := time.Now().UTC().Add(24 * time.Hour)
		_ = manager.db.Model(&models.Certificate{}).Where("id = ?", task.CertificateID).Updates(map[string]any{
			"last_error":    message,
			"next_renew_at": retryAt,
		}).Error
	}
	if task.ManagedID != "" && (task.Operation == models.CertificateTaskOperationManagedIssue || task.Operation == models.CertificateTaskOperationManagedRenew) {
		retryAt := time.Now().UTC().Add(24 * time.Hour)
		_ = manager.db.Model(&models.ManagedCertificate{}).Where("id = ?", task.ManagedID).Updates(map[string]any{
			"status":        models.CertificateStatusError,
			"last_error":    message,
			"next_renew_at": retryAt,
		}).Error
	}
}

func isManagedCertificateTask(operation string) bool {
	switch operation {
	case models.CertificateTaskOperationIssue,
		models.CertificateTaskOperationRenew,
		models.CertificateTaskOperationUpload,
		models.CertificateTaskOperationBind,
		models.CertificateTaskOperationManagedIssue,
		models.CertificateTaskOperationManagedRenew:
		return true
	default:
		return false
	}
}

// SafeCertificateErrorDetail turns expected certificate environment failures
// into actionable text without exposing absolute paths or raw lower-level
// errors through task APIs.
func SafeCertificateErrorDetail(err error) string {
	if err == nil {
		return "证书操作失败，请查看任务日志。"
	}
	lower := strings.ToLower(err.Error())
	switch {
	case strings.Contains(lower, "website_web_server_mismatch"):
		return "网站所属 Web Server 与当前运行实例不一致，请切换回网站所属 Web Server 后重试。"
	case strings.Contains(lower, "certificate and private key are required"):
		return "证书或私钥文件为空，请重新上传包含证书和私钥的完整材料。"
	case strings.Contains(lower, "certificate must be pem encoded"),
		strings.Contains(lower, "certificate file is invalid"),
		strings.Contains(lower, "parse certificate"),
		strings.Contains(lower, "x509:"):
		return "证书文件格式不可用，请上传 PEM 编码的有效证书文件。"
	case strings.Contains(lower, "certificate is expired"):
		return "证书已过期，请重新申请或上传未过期的证书。"
	case strings.Contains(lower, "certificate is not valid yet"):
		return "证书尚未生效，请检查服务器时间或稍后重试。"
	case strings.Contains(lower, "certificate and private key do not match"):
		return "证书与私钥不匹配，请重新上传同一证书对应的私钥。"
	case strings.Contains(lower, "certificate material path is outside"),
		strings.Contains(lower, "certificate files are outside"):
		return "证书资源文件位置无效，请重新导入证书资源。"
	case strings.Contains(lower, "caddy runtime config"),
		strings.Contains(lower, "caddy main config"),
		strings.Contains(lower, "caddy managed config"),
		strings.Contains(lower, "web server config directory"):
		return "Caddy 配置目录或主配置文件不可用，请检查 Caddy 安装、服务配置和目录权限后重试。"
	case strings.Contains(lower, "web server config validation failed"):
		return "Web Server 配置校验失败，请检查当前站点配置及证书文件后重试。"
	case strings.Contains(lower, "web server reload failed"):
		return "Web Server 重载失败，当前配置已回滚，请检查服务状态和错误日志后重试。"
	case strings.Contains(lower, "does not cover domain"),
		strings.Contains(lower, "does not cover website domain"):
		return "现有证书不包含网站的全部域名，请重新上传或签发同时覆盖这些域名的证书。"
	case strings.Contains(lower, "新域名不在当前证书范围内"):
		return "现有证书不包含网站的全部域名，请重新上传或签发同时覆盖这些域名的证书。"
	case strings.Contains(lower, "website is disabled"), strings.Contains(lower, "网站已停用"):
		return "网站已停用，请先启用网站后再绑定证书。"
	case isDNSLookupNotFoundError(lower):
		domain := certificateErrorDomain(err.Error())
		if domain != "" {
			return fmt.Sprintf("域名 %s 没有有效的 A/AAAA DNS 记录，请先将域名解析到本服务器后再申请 HTTP-01 证书。", domain)
		}
		return "域名没有有效的 A/AAAA DNS 记录，请先将域名解析到本服务器后再申请 HTTP-01 证书。"
	case certificateHTTPResponseStatus(err.Error()) == 403:
		domain := certificateErrorDomain(err.Error())
		if domain != "" {
			return fmt.Sprintf("域名 %s 的 HTTP-01 验证地址返回 403，请检查 DNS 是否指向本服务器，以及 CDN/WAF 是否拦截了验证请求。", domain)
		}
		return "HTTP-01 验证地址返回 403，请检查 DNS 是否指向本服务器，以及 CDN/WAF 是否拦截了验证请求。"
	case certificateHTTPResponseStatus(err.Error()) == 404:
		domain := certificateErrorDomain(err.Error())
		if domain != "" {
			return fmt.Sprintf("域名 %s 的 HTTP-01 验证地址返回 404，请检查网站是否启用、80 端口是否可访问，以及 ACME 验证路由是否已发布。", domain)
		}
		return "HTTP-01 验证地址返回 404，请检查网站是否启用、80 端口是否可访问，以及 ACME 验证路由是否已发布。"
	case strings.Contains(lower, "publish http-01 route"):
		return "HTTP-01 验证路由发布失败，请检查网站所属 Web Server、配置校验和服务状态后重试。"
	case strings.Contains(lower, "permission denied"), strings.Contains(lower, "operation not permitted"):
		return "证书文件或存储目录不可写，请检查面板运行用户的目录权限后重试。"
	case strings.Contains(lower, "no such file or directory"):
		return "证书存储目录或依赖文件不存在，请检查证书目录配置和磁盘状态后重试。"
	case strings.Contains(lower, "database is locked"), strings.Contains(lower, "resource busy"):
		return "证书资源正被其他操作占用，请等待当前任务完成后重试。"
	case strings.Contains(lower, "certificate directory is invalid"):
		return "证书存储目录配置无效，请检查目录是否为绝对路径并且可用。"
	case strings.Contains(lower, "certificate database is not initialized"):
		return "证书数据库尚未初始化，请检查面板启动状态和数据库配置。"
	case strings.Contains(lower, "certificate issuer is not configured"):
		return "证书签发器未配置，请检查证书任务服务配置后重试。"
	case strings.Contains(lower, "certificate deployer is not configured"):
		return "证书部署器未配置，请检查证书任务服务配置后重试。"
	case strings.Contains(lower, "dns account"), strings.Contains(lower, "dns provider"), strings.Contains(lower, "cloudflare"), strings.Contains(lower, "aliyun"), strings.Contains(lower, "tencent"):
		return "DNS 账号或 DNS 挑战处理失败，请检查账号凭据、权限及域名 DNS 托管是否匹配后重试。"
	case strings.Contains(lower, "dns challenge"), strings.Contains(lower, "validate domain"), strings.Contains(lower, "acme server"), strings.Contains(lower, "create acme order"):
		return "ACME 域名验证失败，请检查域名解析、验证方式和 CA 服务状态后重试。"
	case strings.Contains(lower, "acme directory url is required"):
		return "ACME 目录地址未配置，证书任务服务无法启动，请检查 ACME 配置后重试。"
	case strings.Contains(lower, "acme issue timeout must be between"):
		return "ACME 任务超时配置无效，请将超时时间设置为 1 到 120 分钟。"
	case strings.Contains(lower, "acme challenge directory is invalid"):
		return "ACME 挑战目录配置无效，请检查目录是否为可用的绝对路径。"
	case strings.Contains(lower, "certificate task queue is full"):
		return "证书任务队列已满，请等待已有任务完成后重试。"
	case strings.Contains(lower, "certificate task manager is stopping"):
		return "证书任务服务正在停止，请稍后重试。"
	case strings.Contains(lower, "already has an active certificate task"):
		return "当前已有证书任务正在执行，请等待任务完成后重试。"
	case strings.Contains(lower, "does not cover domain"):
		return "现有证书不包含目标域名，请重新上传覆盖该域名的证书。"
	default:
		return "证书资源创建失败，请查看证书任务详情或任务日志获取具体原因。"
	}
}

func isDNSLookupNotFoundError(lower string) bool {
	return strings.Contains(lower, "no a or aaaa record") ||
		strings.Contains(lower, "nxdomain") ||
		strings.Contains(lower, "no such host")
}

func certificateHTTPResponseStatus(value string) int {
	lower := strings.ToLower(value)
	if marker := "returned http "; strings.Contains(lower, marker) {
		start := strings.Index(lower, marker) + len(marker)
		fields := strings.Fields(lower[start:])
		if len(fields) == 0 {
			return 0
		}
		status, _ := strconv.Atoi(fields[0])
		return status
	}
	marker := "invalid response from"
	start := strings.Index(lower, marker)
	if start < 0 {
		return 0
	}
	response := lower[start+len(marker):]
	separator := strings.Index(response, ": ")
	if separator < 0 {
		return 0
	}
	fields := strings.Fields(response[separator+2:])
	if len(fields) == 0 {
		return 0
	}
	status, _ := strconv.Atoi(fields[0])
	return status
}

func certificateErrorDomain(value string) string {
	lower := strings.ToLower(value)
	markers := []string{"validate domain ", "http-01 challenge self-check for "}
	for _, marker := range markers {
		start := strings.Index(lower, marker)
		if start < 0 {
			continue
		}
		start += len(marker)
		end := strings.IndexAny(value[start:], ": ")
		if end < 0 {
			end = len(value) - start
		}
		domain := strings.TrimSpace(value[start : start+end])
		if domain != "" {
			return domain
		}
	}
	return ""
}

func (manager *Manager) finish(taskID, status, code, message string) error {
	now := time.Now().UTC()
	progress := 100
	if status != models.CertificateTaskStatusSucceeded {
		progress = 0
		var task models.CertificateTask
		if err := manager.db.Select("progress").First(&task, "id = ?", taskID).Error; err == nil {
			progress = task.Progress
		}
	}
	return manager.db.Model(&models.CertificateTask{}).Where("id = ?", taskID).Updates(map[string]any{
		"status":        status,
		"progress":      progress,
		"message":       truncate(message, 512),
		"error_code":    code,
		"error_message": truncate(message, 1024),
		"finished_at":   now,
	}).Error
}

func (manager *Manager) appendLog(taskID, message string) {
	message = strings.ReplaceAll(strings.TrimSpace(message), "\x00", "")
	if message == "" {
		return
	}
	var task models.CertificateTask
	if err := manager.db.Select("log_text").First(&task, "id = ?", taskID).Error; err != nil {
		return
	}
	line := fmt.Sprintf("[%s] %s\n", time.Now().UTC().Format(time.RFC3339), message)
	task.LogText += line
	if len(task.LogText) > maxCertificateLog {
		task.LogText = "[... earlier certificate log truncated ...]\n" +
			task.LogText[len(task.LogText)-maxCertificateLog+48:]
	}
	_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", taskID).
		Update("log_text", task.LogText).Error
}

func (manager *Manager) GetCertificateByWebsite(websiteID int64) (*models.Certificate, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	var certificate models.Certificate
	if err := manager.db.First(&certificate, "website_id = ?", websiteID).Error; err != nil {
		return nil, err
	}
	if certificate.Status != models.CertificateStatusDisabled &&
		certificate.Status != models.CertificateStatusError {
		certificate.Status = certificateStatus(
			certificate.NotAfter,
			time.Now().UTC(),
			certificate.RenewBeforeDays,
		)
	}
	return &certificate, nil
}

func (manager *Manager) GetTask(taskID string) (*models.CertificateTask, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	return getCertificateTask(manager.db, taskID)
}

func (manager *Manager) ListTasks(options TaskListOptions) (*TaskList, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	return listCertificateTasks(manager.db, options)
}

func getCertificateTask(db *gorm.DB, taskID string) (*models.CertificateTask, error) {
	var task models.CertificateTask
	if err := db.First(&task, "id = ?", strings.TrimSpace(taskID)).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func listCertificateTasks(db *gorm.DB, options TaskListOptions) (*TaskList, error) {
	options.Page, options.PageSize = normalizePage(options.Page, options.PageSize)
	query := db.Model(&models.CertificateTask{})
	if options.WebsiteID > 0 {
		query = query.Where("website_id = ?", options.WebsiteID)
	}
	if status := strings.TrimSpace(options.Status); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, err
	}
	var tasks []models.CertificateTask
	if err := query.Order("created_at DESC").
		Offset((options.Page - 1) * options.PageSize).
		Limit(options.PageSize).
		Find(&tasks).Error; err != nil {
		return nil, err
	}
	return &TaskList{Data: tasks, Total: total, Page: options.Page, PageSize: options.PageSize}, nil
}

func normalizePage(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}

func (manager *Manager) ReadTaskLog(taskID string) (*LogResult, error) {
	return readCertificateTaskLog(manager.db, taskID)
}

func readCertificateTaskLog(db *gorm.DB, taskID string) (*LogResult, error) {
	var task models.CertificateTask
	if err := db.Select("status", "log_text").First(&task, "id = ?", strings.TrimSpace(taskID)).Error; err != nil {
		return nil, err
	}
	return &LogResult{
		Content: task.LogText,
		EOF:     models.IsCertificateTaskTerminal(task.Status),
	}, nil
}

func (manager *Manager) Cancel(taskID string) (*models.CertificateTask, error) {
	task, err := manager.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if models.IsCertificateTaskTerminal(task.Status) {
		return task, nil
	}
	if task.Status == models.CertificateTaskStatusQueued {
		manager.cleanupTaskInput(task)
		if err := manager.finish(task.ID, models.CertificateTaskStatusCanceled, "TASK_CANCELED", "证书任务已取消"); err != nil {
			return nil, err
		}
	} else {
		if err := manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).Updates(map[string]any{
			"status":           models.CertificateTaskStatusCanceling,
			"cancel_requested": true,
			"message":          "正在取消证书任务",
		}).Error; err != nil {
			return nil, err
		}
		manager.cancelMu.Lock()
		cancel := manager.cancels[task.ID]
		manager.cancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	return manager.GetTask(task.ID)
}

func (manager *Manager) cleanupTaskInput(task *models.CertificateTask) {
	if task == nil || task.InputCertPath == "" {
		return
	}
	_ = os.RemoveAll(filepath.Dir(task.InputCertPath))
	_ = manager.db.Model(&models.CertificateTask{}).Where("id = ?", task.ID).
		Updates(map[string]any{"input_cert_path": "", "input_key_path": ""}).Error
}

func (manager *Manager) Disable(websiteID int64) (*models.Certificate, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	manager.submitMu.Lock()
	defer manager.submitMu.Unlock()
	var active int64
	if err := manager.db.Model(&models.CertificateTask{}).
		Where("website_id = ? AND status IN ?", websiteID, models.ActiveCertificateTaskStatuses()).
		Count(&active).Error; err != nil {
		return nil, err
	}
	if active > 0 {
		return nil, errors.New("website has an active certificate task")
	}
	var certificate models.Certificate
	if err := manager.db.First(&certificate, "website_id = ?", websiteID).Error; err != nil {
		return nil, err
	}
	deployer := manager.currentDeployer()
	if deployer == nil {
		return nil, errors.New("certificate deployer is not configured")
	}
	rollback, err := deployer.Disable(context.Background(), websiteID)
	if err != nil {
		return nil, err
	}
	err = manager.db.Model(&models.Certificate{}).Where("id = ?", certificate.ID).Updates(map[string]any{
		"status":        models.CertificateStatusDisabled,
		"auto_renew":    false,
		"force_https":   false,
		"next_renew_at": nil,
	}).Error
	if err != nil {
		_ = rollback(context.Background())
		return nil, err
	}
	return manager.GetCertificateByWebsite(websiteID)
}

func (manager *Manager) Stop(ctx context.Context) error {
	manager.stopOnce.Do(func() {
		manager.stopping.Store(true)
		close(manager.stopCh)
		manager.cancelMu.Lock()
		for _, cancel := range manager.cancels {
			cancel()
		}
		manager.cancelMu.Unlock()
	})
	done := make(chan struct{})
	go func() {
		manager.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func normalizeEmail(value string) (string, error) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || !strings.EqualFold(address.Address, value) || len(value) > 254 {
		return "", errors.New("valid ACME account email is required")
	}
	return strings.ToLower(value), nil
}

func normalizeChallengeType(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = ChallengeHTTP01
	}
	if value != ChallengeHTTP01 && value != ChallengeDNS01 {
		return "", errors.New("unsupported ACME challenge type")
	}
	return value, nil
}

func defaultChallengeType(value string) string {
	if strings.TrimSpace(value) == "" {
		return ChallengeHTTP01
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeACMEDomains(values []string, challengeType string) ([]string, error) {
	domains, err := normalizeManagedDomains(values)
	if err != nil {
		return nil, err
	}
	for _, domain := range domains {
		base := strings.TrimPrefix(domain, "*.")
		if net.ParseIP(base) != nil {
			return nil, errors.New("public ACME certificate issuance requires domain names, not IP addresses")
		}
		if strings.Contains(domain, "*") &&
			(challengeType != ChallengeDNS01 || !strings.HasPrefix(domain, "*.") || strings.Count(domain, "*") != 1) {
			return nil, errors.New("wildcard domains require DNS-01 and must use the *.example.com format")
		}
	}
	return domains, nil
}

func certificateDomains(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	domains := make([]string, 0, len(parts))
	seen := make(map[string]struct{})
	for _, part := range parts {
		domain := strings.ToLower(strings.TrimSpace(part))
		if domain == "" {
			continue
		}
		if strings.HasPrefix(domain, "*.") {
			return nil, errors.New("HTTP-01 does not support wildcard domains; configure DNS-01 in a future provider")
		}
		if net.ParseIP(domain) != nil {
			return nil, errors.New("public ACME certificate issuance requires domain names, not IP addresses")
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	if len(domains) == 0 || len(domains) > 100 {
		return nil, errors.New("website must contain between 1 and 100 certificate domains")
	}
	return domains, nil
}

func sameDomains(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		values[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range right {
		if _, exists := values[strings.ToLower(strings.TrimSpace(value))]; !exists {
			return false
		}
	}
	return true
}

func validateIssuedCertificate(
	issued *IssuedCertificate,
	domains []string,
) (*x509.Certificate, error) {
	if issued == nil || len(issued.CertificatePEM) == 0 || len(issued.PrivateKeyPEM) == 0 {
		return nil, errors.New("certificate issuer returned incomplete material")
	}
	pair, err := tls.X509KeyPair(issued.CertificatePEM, issued.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("certificate and private key do not match: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, errors.New("certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse issued certificate: %w", err)
	}
	now := time.Now().UTC()
	if leaf.NotAfter.Before(now.Add(time.Hour)) || leaf.NotBefore.After(now.Add(10*time.Minute)) {
		return nil, errors.New("issued certificate validity period is not usable")
	}
	for _, domain := range domains {
		if !certificateMatchesDomain(leaf, domain) {
			return nil, fmt.Errorf("issued certificate does not cover %s", domain)
		}
	}
	block, _ := pem.Decode(issued.CertificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("issued certificate is not valid PEM")
	}
	return leaf, nil
}

func certificateStatus(notAfter, now time.Time, renewBeforeDays int) string {
	if !notAfter.After(now) {
		return models.CertificateStatusExpired
	}
	if notAfter.Before(now.Add(time.Duration(renewBeforeDays) * 24 * time.Hour)) {
		return models.CertificateStatusExpiring
	}
	return models.CertificateStatusActive
}

func truncate(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func (manager *Manager) removeSupersededVersion(
	oldCertificatePath, oldPrivateKeyPath, currentCertificatePath string,
) error {
	if strings.TrimSpace(oldCertificatePath) == "" ||
		strings.TrimSpace(oldPrivateKeyPath) == "" {
		return nil
	}
	oldDirectory := filepath.Dir(filepath.Clean(oldCertificatePath))
	if oldDirectory != filepath.Dir(filepath.Clean(oldPrivateKeyPath)) ||
		oldDirectory == filepath.Dir(filepath.Clean(currentCertificatePath)) {
		return nil
	}
	managedSitesRoot := filepath.Join(manager.certificateRoot, "sites")
	relative, err := filepath.Rel(managedSitesRoot, oldDirectory)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("old certificate version is outside the managed directory")
	}
	info, err := os.Lstat(oldDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("old certificate version is not a managed directory")
	}
	return os.RemoveAll(oldDirectory)
}
