package bastion

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"oneinstack/internal/models"
	"oneinstack/utils"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"
)

// CreateServerInput 添加服务器输入
type CreateServerInput struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	AuthMethod string `json:"authMethod"`
	Password   string `json:"password"`
	KeyPath    string `json:"keyPath,omitempty"`
	Tags       string `json:"tags,omitempty"`
}

// UpdateServerInput 编辑服务器输入
type UpdateServerInput struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	AuthMethod string `json:"authMethod"`
	Password   string `json:"password,omitempty"`
	KeyPath    string `json:"keyPath,omitempty"`
	Tags       string `json:"tags,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// Manager 堡垒机管理器
type Manager struct {
	db             *gorm.DB
	collector      Collector
	collectTimeout int
	maxConcurrent  int
	retentionDays  int
	scheduler      *cron.Cron
	now            func() time.Time
	mu             sync.Mutex
	background     sync.WaitGroup
	startOnce      sync.Once
	stopOnce       sync.Once
}

var defaultManager struct {
	sync.RWMutex
	value *Manager
}

// NewManager 创建堡垒机管理器
func NewManager(
	db *gorm.DB,
	collector Collector,
	collectTimeoutSeconds, maxConcurrent, retentionDays int,
	collectSchedule, cleanupSchedule string,
) (*Manager, error) {
	if db == nil {
		return nil, errors.New("database is not initialized")
	}
	if collector == nil {
		return nil, errors.New("collector is required")
	}
	if collectTimeoutSeconds < 1 || collectTimeoutSeconds > 120 {
		return nil, errors.New("collect timeout must be between 1 and 120 seconds")
	}
	if maxConcurrent < 1 || maxConcurrent > 20 {
		return nil, errors.New("max concurrent collects must be between 1 and 20")
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return nil, errors.New("retention must be between 1 and 3650 days")
	}
	scheduler := cron.New(cron.WithChain(
		cron.SkipIfStillRunning(cron.DefaultLogger),
		cron.Recover(cron.DefaultLogger),
	))
	manager := &Manager{
		db:             db,
		collector:      collector,
		collectTimeout: collectTimeoutSeconds,
		maxConcurrent:  maxConcurrent,
		retentionDays:  retentionDays,
		scheduler:      scheduler,
		now:            time.Now,
	}
	if _, err := scheduler.AddFunc(collectSchedule, func() {
		manager.collectAllServers()
	}); err != nil {
		return nil, fmt.Errorf("invalid bastion collect schedule: %w", err)
	}
	if _, err := scheduler.AddFunc(cleanupSchedule, func() {
		if err := manager.Cleanup(); err != nil {
			log.Printf("bastion metric cleanup failed: %v", err)
		}
	}); err != nil {
		return nil, fmt.Errorf("invalid bastion cleanup schedule: %w", err)
	}
	return manager, nil
}

// ConfigureDefault 设置全局默认管理器
func ConfigureDefault(manager *Manager) {
	defaultManager.Lock()
	defaultManager.value = manager
	defaultManager.Unlock()
}

// Default 获取全局默认管理器
func Default() *Manager {
	defaultManager.RLock()
	defer defaultManager.RUnlock()
	return defaultManager.value
}

// Start 启动采集调度
func (m *Manager) Start() {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		m.scheduler.Start()
		m.background.Add(1)
		go func() {
			defer m.background.Done()
			m.collectAllServers()
		}()
	})
}

// Stop 停止采集调度
func (m *Manager) Stop(ctx context.Context) error {
	if m == nil {
		return nil
	}
	var stopped context.Context
	m.stopOnce.Do(func() { stopped = m.scheduler.Stop() })
	if stopped == nil {
		return nil
	}
	finished := make(chan struct{})
	go func() {
		<-stopped.Done()
		m.background.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// collectAllServers 采集所有启用服务器的指标
func (m *Manager) collectAllServers() {
	var servers []models.BastionServer
	if err := m.db.Where("enabled = ?", true).Find(&servers).Error; err != nil {
		log.Printf("bastion: list servers: %v", err)
		return
	}
	if len(servers) == 0 {
		return
	}

	sem := make(chan struct{}, m.maxConcurrent)
	var wg sync.WaitGroup

	for i := range servers {
		wg.Add(1)
		go func(server *models.BastionServer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			m.collectOne(server)
		}(&servers[i])
	}
	wg.Wait()
}

func (m *Manager) collectOne(server *models.BastionServer) {
	var password string
	if server.AuthMethod == models.BastionAuthPassword {
		decrypted, err := decryptPassword(server.PasswordEnc)
		if err != nil {
			m.updateServerStatus(server, models.BastionStatusError, fmt.Sprintf("password decrypt: %v", err))
			return
		}
		password = decrypted
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(m.collectTimeout)*time.Second)
	defer cancel()

	sample, err := m.collector.Collect(ctx, server, password)
	now := m.now().UTC()
	server.LastSeenAt = &now

	if err != nil {
		m.updateServerStatus(server, models.BastionStatusError, err.Error())
		return
	}

	sample.ServerID = server.ID
	if err := m.db.Create(sample).Error; err != nil {
		log.Printf("bastion: persist metric for %s: %v", server.Name, err)
		return
	}

	m.updateServerStatus(server, models.BastionStatusOnline, "")
	server.OSInfo = "" // will be set via probe separately
	_ = m.db.Model(server).Updates(map[string]interface{}{
		"status":       models.BastionStatusOnline,
		"status_error": "",
		"last_seen_at": now,
	}).Error
}

func (m *Manager) updateServerStatus(server *models.BastionServer, status, statusError string) {
	now := m.now().UTC()
	updateMap := map[string]interface{}{
		"status":       status,
		"status_error": truncate(statusError, 512),
		"last_seen_at": now,
	}
	if server.Status != status || server.StatusError != statusError {
		if err := m.db.Model(server).Updates(updateMap).Error; err != nil {
			log.Printf("bastion: update status for %s: %v", server.Name, err)
		}
	}
}

// Cleanup 清理过期指标数据
func (m *Manager) Cleanup() error {
	cutoff := m.now().UTC().Add(-time.Duration(m.retentionDays) * 24 * time.Hour)
	result := m.db.Where("captured_at < ?", cutoff).Delete(&models.BastionMetricSample{})
	if result.Error != nil {
		return fmt.Errorf("bastion cleanup: %w", result.Error)
	}
	if result.RowsAffected > 0 {
		log.Printf("bastion: cleaned %d metric samples older than %s", result.RowsAffected, cutoff.Format(time.RFC3339))
	}
	return nil
}

// ListServers 返回服务器列表及最新指标摘要
func (m *Manager) ListServers() ([]models.BastionServerSummary, error) {
	var servers []models.BastionServer
	if err := m.db.Order("id ASC").Find(&servers).Error; err != nil {
		return nil, err
	}
	result := make([]models.BastionServerSummary, 0, len(servers))
	for _, server := range servers {
		summary := models.BastionServerSummary{BastionServer: server}
		var latest models.BastionMetricSample
		err := m.db.Where("server_id = ?", server.ID).
			Order("captured_at DESC").Limit(1).First(&latest).Error
		if err == nil {
			summary.LatestCPU = &latest.CPUPercent
			summary.LatestMemory = &latest.MemoryPercent
			summary.LatestDisk = &latest.DiskPercent
			summary.LatestNetworkRecv = &latest.NetworkReceiveBPS
			summary.LatestNetworkSend = &latest.NetworkSendBPS
			summary.LatestCapturedAt = &latest.CapturedAt
		}
		result = append(result, summary)
	}
	return result, nil
}

// GetServer 获取单个服务器详情
func (m *Manager) GetServer(id uint) (*models.BastionServer, error) {
	var server models.BastionServer
	if err := m.db.First(&server, id).Error; err != nil {
		return nil, err
	}
	return &server, nil
}

// CreateServer 添加远端服务器
func (m *Manager) CreateServer(input CreateServerInput) (*models.BastionServer, error) {
	if input.AuthMethod == "" {
		input.AuthMethod = models.BastionAuthPassword
	}
	if err := validateServerInput(input.Name, input.Host, input.Port, input.Username, input.AuthMethod); err != nil {
		return nil, err
	}
	if input.AuthMethod == models.BastionAuthPassword && input.Password == "" {
		return nil, errors.New("密码不能为空")
	}
	passwordEnc, err := encryptPassword(input.Password)
	if err != nil {
		return nil, fmt.Errorf("encrypt password: %w", err)
	}
	if input.Port == 0 {
		input.Port = 22
	}
	server := &models.BastionServer{
		Name:        input.Name,
		Host:        input.Host,
		Port:        input.Port,
		Username:    input.Username,
		AuthMethod:  input.AuthMethod,
		PasswordEnc: passwordEnc,
		KeyPath:     input.KeyPath,
		Tags:        input.Tags,
		Enabled:     true,
		Status:      models.BastionStatusUnknown,
	}
	if err := m.db.Create(server).Error; err != nil {
		return nil, err
	}
	return server, nil
}

// UpdateServer 编辑远端服务器
func (m *Manager) UpdateServer(id uint, input UpdateServerInput) (*models.BastionServer, error) {
	var server models.BastionServer
	if err := m.db.First(&server, id).Error; err != nil {
		return nil, err
	}
	if input.AuthMethod == "" {
		input.AuthMethod = server.AuthMethod
	}
	if err := validateServerInput(input.Name, input.Host, input.Port, input.Username, input.AuthMethod); err != nil {
		return nil, err
	}
	if input.AuthMethod == models.BastionAuthPassword && input.Password == "" && server.PasswordEnc == "" {
		return nil, errors.New("密码不能为空")
	}
	server.Name = input.Name
	server.Host = input.Host
	server.Port = input.Port
	server.Username = input.Username
	server.AuthMethod = input.AuthMethod
	server.KeyPath = input.KeyPath
	server.Tags = input.Tags
	if input.Password != "" {
		passwordEnc, err := encryptPassword(input.Password)
		if err != nil {
			return nil, fmt.Errorf("encrypt password: %w", err)
		}
		server.PasswordEnc = passwordEnc
	}
	if input.Enabled != nil {
		server.Enabled = *input.Enabled
	}
	if err := m.db.Save(&server).Error; err != nil {
		return nil, err
	}
	return &server, nil
}

// DeleteServer 删除远端服务器及其指标数据
func (m *Manager) DeleteServer(id uint) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("server_id = ?", id).Delete(&models.BastionMetricSample{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.BastionServer{}, id).Error; err != nil {
			return err
		}
		return nil
	})
}

// TestConnection 测试与指定服务器的连通性。
// password 为空时回退到库中已保存的密码（key 认证使用已配置的私钥路径）。
func (m *Manager) TestConnection(id uint, password string) (*ProbeResult, error) {
	var server models.BastionServer
	if err := m.db.First(&server, id).Error; err != nil {
		return nil, err
	}
	if password == "" && server.AuthMethod == models.BastionAuthPassword {
		decrypted, err := decryptPassword(server.PasswordEnc)
		if err != nil {
			return nil, fmt.Errorf("读取已保存的密码失败: %w", err)
		}
		password = decrypted
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return Probe(ctx, &server, password), nil
}

// GetMetrics 获取指定服务器的历史指标
func (m *Manager) GetMetrics(serverID uint, from, to time.Time, limit int) ([]models.BastionMetricSample, error) {
	if limit <= 0 || limit > 2000 {
		limit = 200
	}
	query := m.db.Where("server_id = ?", serverID).Order("captured_at DESC").Limit(limit)
	if !from.IsZero() {
		query = query.Where("captured_at >= ?", from)
	}
	if !to.IsZero() {
		query = query.Where("captured_at <= ?", to)
	}
	var samples []models.BastionMetricSample
	if err := query.Find(&samples).Error; err != nil {
		return nil, err
	}
	return samples, nil
}

func validateServerInput(name, host string, port int, username, authMethod string) error {
	if name == "" {
		return errors.New("服务器名称不能为空")
	}
	if host == "" {
		return errors.New("IP 地址不能为空")
	}
	if port < 1 || port > 65535 {
		return errors.New("端口范围 1-65535")
	}
	if username == "" {
		return errors.New("用户名不能为空")
	}
	if authMethod != "" && authMethod != models.BastionAuthPassword && authMethod != models.BastionAuthKey {
		return errors.New("认证方式必须是 password 或 key")
	}
	return nil
}

func encryptPassword(password string) (string, error) {
	if password == "" {
		return "", nil
	}
	return utils.EncryptCredential(password, utils.CredentialPurposeBastionPassword)
}

func decryptPassword(encrypted string) (string, error) {
	if encrypted == "" {
		return "", errors.New("password is not configured")
	}
	return utils.DecryptCredential(encrypted, utils.CredentialPurposeBastionPassword)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen]
}
