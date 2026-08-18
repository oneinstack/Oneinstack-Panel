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

	startOnce sync.Once
	startErr  error
	stopOnce  sync.Once
	submitMu  sync.Mutex
	cancelMu  sync.Mutex
	cancels   map[string]context.CancelFunc
	runWG     sync.WaitGroup
	stopping  atomic.Bool
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

func (manager *Manager) Start() error {
	manager.startOnce.Do(func() {
		switch {
		case manager.db == nil:
			manager.startErr = errors.New("certificate database is not initialized")
		case manager.issuer == nil:
			manager.startErr = errors.New("certificate issuer is not configured")
		case manager.deployer == nil:
			manager.startErr = errors.New("certificate deployer is not configured")
		case invalidPathRoot(manager.certificateRoot):
			manager.startErr = errors.New("certificate directory is invalid")
		case invalidPathRoot(manager.challengeRoot):
			manager.startErr = errors.New("ACME challenge directory is invalid")
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
		if err := os.MkdirAll(manager.challengeRoot, 0755); err != nil {
			manager.startErr = fmt.Errorf("create ACME challenge directory: %w", err)
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
		AutoRenew:       certificate.AutoRenew,
		RenewBeforeDays: certificate.RenewBeforeDays,
		ForceHTTPS:      certificate.ForceHTTPS,
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
	if err := manager.deployer.EnsureChallenge(ctx, task.WebsiteID); err != nil {
		manager.failTask(&task, "CHALLENGE_CONFIG_FAILED", fmt.Errorf("publish HTTP-01 route: %w", err))
		return
	}
	directoryHash := sha256.Sum256([]byte(task.DirectoryURL))
	issued, err := manager.issuer.Issue(ctx, IssueRequest{
		DirectoryURL:   task.DirectoryURL,
		Email:          task.Email,
		Domains:        strings.Split(task.Domains, ","),
		AccountKeyPath: filepath.Join(manager.certificateRoot, "accounts", hex.EncodeToString(directoryHash[:16])+".key"),
		ChallengeRoot:  manager.challengeRoot,
	}, report)
	if err != nil {
		manager.failTask(&task, "ACME_ISSUE_FAILED", err)
		return
	}
	if err := ctx.Err(); err != nil {
		manager.failTask(&task, "TASK_CANCELED", err)
		return
	}
	metadata, err := validateIssuedCertificate(issued, strings.Split(task.Domains, ","))
	if err != nil {
		manager.failTask(&task, "CERTIFICATE_INVALID", err)
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
	report(96, "正在验证并重新加载 Nginx")
	rollback, err := manager.deployer.Deploy(
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
			CertificatePath: certificateRecord.CertificatePath, PrivateKeyPath: certificateRecord.PrivateKeyPath,
			SerialNumber: certificateRecord.SerialNumber, Issuer: certificateRecord.Issuer,
			Algorithm: publicKeyAlgorithm(metadata.PublicKey), Status: certificateRecord.Status,
			AutoRenew: certificateRecord.AutoRenew, RenewBeforeDays: certificateRecord.RenewBeforeDays,
			NotBefore: certificateRecord.NotBefore, NotAfter: certificateRecord.NotAfter,
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

func (manager *Manager) runManagedTask(ctx context.Context, task *models.CertificateTask, report ProgressReporter) {
	catalog := NewCatalog(manager.db, manager.certificateRoot, manager.deployer)
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
	var task models.CertificateTask
	if err := manager.db.First(&task, "id = ?", strings.TrimSpace(taskID)).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func (manager *Manager) ListTasks(options TaskListOptions) (*TaskList, error) {
	if err := manager.Start(); err != nil {
		return nil, err
	}
	options.Page, options.PageSize = normalizePage(options.Page, options.PageSize)
	query := manager.db.Model(&models.CertificateTask{})
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
	var task models.CertificateTask
	if err := manager.db.Select("status", "log_text").First(&task, "id = ?", strings.TrimSpace(taskID)).Error; err != nil {
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
	rollback, err := manager.deployer.Disable(context.Background(), websiteID)
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
		if err := leaf.VerifyHostname(domain); err != nil {
			return nil, fmt.Errorf("issued certificate does not cover %s: %w", domain, err)
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
