package cluster

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"oneinstack/internal/models"

	"gorm.io/gorm"
)

var (
	ErrNodeNotFound     = gorm.ErrRecordNotFound
	ErrInvalidToken     = errors.New("invalid node token")
	ErrNodeDisabled     = errors.New("node is disabled")
	ErrNodeDeparted     = errors.New("node has left the cluster and must register again")
	ErrNameRequired     = errors.New("node name is required")
	ErrEndpointInvalid  = errors.New("node endpoint must be a valid http or https URL")
	ErrNodeFieldTooLong = errors.New("node field is too long")
)

type Manager struct{ db *gorm.DB }

func NewManager(db *gorm.DB) (*Manager, error) {
	if db == nil {
		return nil, errors.New("database is not initialized")
	}
	return &Manager{db: db}, nil
}

type CreateNodeInput struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Group    string `json:"group,omitempty"`
	Tags     string `json:"tags,omitempty"`
}

type UpdateNodeInput struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Group    string `json:"group,omitempty"`
	Tags     string `json:"tags,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

type HostSnapshot struct {
	CPUPercent       float64 `json:"cpuPercent"`
	CPUTotalCores    int     `json:"cpuTotalCores"`
	CPUUsedCores     float64 `json:"cpuUsedCores"`
	MemoryPercent    float64 `json:"memoryPercent"`
	MemoryUsedBytes  uint64  `json:"memoryUsedBytes"`
	MemoryTotalBytes uint64  `json:"memoryTotalBytes"`
	DiskPercent      float64 `json:"diskPercent"`
	DiskUsedBytes    uint64  `json:"diskUsedBytes"`
	DiskTotalBytes   uint64  `json:"diskTotalBytes"`
	NetworkRecvBPS   float64 `json:"networkReceiveBps"`
	NetworkSendBPS   float64 `json:"networkSendBps"`
	UptimeSeconds    uint64  `json:"uptimeSeconds"`
	IPAddress        string  `json:"ipAddress,omitempty"`
	SubnetMask       string  `json:"subnetMask,omitempty"`
	Gateway          string  `json:"gateway,omitempty"`
	MACAddress       string  `json:"macAddress,omitempty"`
	InterfaceName    string  `json:"interfaceName,omitempty"`
}

type NodeRegistration struct {
	Token                    string   `json:"token"`
	Hostname                 string   `json:"hostname,omitempty"`
	SystemID                 string   `json:"systemId,omitempty"`
	SystemVersion            string   `json:"systemVersion,omitempty"`
	Architecture             string   `json:"architecture,omitempty"`
	PanelVersion             string   `json:"panelVersion,omitempty"`
	AgentVersion             string   `json:"agentVersion,omitempty"`
	Capabilities             []string `json:"capabilities,omitempty"`
	HeartbeatIntervalSeconds int      `json:"heartbeatIntervalSeconds,omitempty"`
	HostSnapshot
}

type NodeHeartbeat struct {
	Token                    string   `json:"token"`
	Hostname                 string   `json:"hostname,omitempty"`
	PanelVersion             string   `json:"panelVersion,omitempty"`
	AgentVersion             string   `json:"agentVersion,omitempty"`
	Capabilities             []string `json:"capabilities,omitempty"`
	HeartbeatIntervalSeconds int      `json:"heartbeatIntervalSeconds,omitempty"`
	CPUPercent               float64  `json:"cpuPercent"`
	MemoryPercent            float64  `json:"memoryPercent"`
	DiskPercent              float64  `json:"diskPercent"`
	NetworkRecvBPS           float64  `json:"networkReceiveBps"`
	NetworkSendBPS           float64  `json:"networkSendBps"`
	UptimeSeconds            uint64   `json:"uptimeSeconds"`
	CPUTotalCores            int      `json:"cpuTotalCores"`
	CPUUsedCores             float64  `json:"cpuUsedCores"`
	MemoryUsedBytes          uint64   `json:"memoryUsedBytes"`
	MemoryTotalBytes         uint64   `json:"memoryTotalBytes"`
	DiskUsedBytes            uint64   `json:"diskUsedBytes"`
	DiskTotalBytes           uint64   `json:"diskTotalBytes"`
	IPAddress                string   `json:"ipAddress,omitempty"`
	SubnetMask               string   `json:"subnetMask,omitempty"`
	Gateway                  string   `json:"gateway,omitempty"`
	MACAddress               string   `json:"macAddress,omitempty"`
	InterfaceName            string   `json:"interfaceName,omitempty"`
}

type CreateNodeResult struct {
	Node  models.ClusterNode `json:"node"`
	Token string             `json:"token"`
}

func (m *Manager) CreateNode(input CreateNodeInput) (CreateNodeResult, error) {
	name := strings.TrimSpace(input.Name)
	endpoint := strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	if name == "" {
		return CreateNodeResult{}, ErrNameRequired
	}
	if len(name) > 120 || len(input.Group) > 120 || len(input.Tags) > 512 {
		return CreateNodeResult{}, ErrNodeFieldTooLong
	}
	if !validEndpoint(endpoint) {
		return CreateNodeResult{}, ErrEndpointInvalid
	}
	token, err := generateToken()
	if err != nil {
		return CreateNodeResult{}, err
	}
	node := models.ClusterNode{Name: name, Endpoint: endpoint, TokenHash: hashToken(token), Enabled: true, Group: strings.TrimSpace(input.Group), Tags: strings.TrimSpace(input.Tags), Status: models.ClusterNodeStatusPending}
	if err := m.db.Create(&node).Error; err != nil {
		return CreateNodeResult{}, err
	}
	return CreateNodeResult{Node: node, Token: token}, nil
}

func (m *Manager) ListNodes() ([]models.ClusterNode, error) {
	if _, err := m.ExpireStaleNodes(time.Now()); err != nil {
		return nil, err
	}
	var nodes []models.ClusterNode
	err := m.db.Order("id asc").Find(&nodes).Error
	return nodes, err
}

// ExpireStaleNodes persists heartbeat-based offline transitions independently
// of whether an operator currently has the cluster page open.
func (m *Manager) ExpireStaleNodes(now time.Time) (int64, error) {
	var nodes []models.ClusterNode
	if err := m.db.Where("enabled = ? AND status = ?", true, models.ClusterNodeStatusOnline).Find(&nodes).Error; err != nil {
		return 0, err
	}
	var expired int64
	for i := range nodes {
		if nodeHeartbeatFresh(nodes[i], now) {
			continue
		}
		result := m.db.Model(&models.ClusterNode{}).
			Where("id = ? AND status = ?", nodes[i].ID, models.ClusterNodeStatusOnline).
			Update("status", models.ClusterNodeStatusOffline)
		if result.Error != nil {
			return expired, result.Error
		}
		expired += result.RowsAffected
	}
	return expired, nil
}

func (m *Manager) GetNode(id uint) (models.ClusterNode, error) {
	var node models.ClusterNode
	err := m.db.First(&node, id).Error
	return node, err
}

func (m *Manager) ListMetrics(id uint, since time.Time, limit int) ([]models.ClusterNodeMetric, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	var metrics []models.ClusterNodeMetric
	query := m.db.Where("node_id = ?", id)
	if !since.IsZero() {
		query = query.Where("captured_at >= ?", since)
	}
	err := query.Order("captured_at desc").Limit(limit).Find(&metrics).Error
	return metrics, err
}

func (m *Manager) UpdateNode(id uint, input UpdateNodeInput) (models.ClusterNode, error) {
	node, err := m.GetNode(id)
	if err != nil {
		return node, err
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return node, ErrNameRequired
	}
	if len(name) > 120 || len(input.Group) > 120 || len(input.Tags) > 512 {
		return node, ErrNodeFieldTooLong
	}
	endpoint := strings.TrimRight(strings.TrimSpace(input.Endpoint), "/")
	if !validEndpoint(endpoint) {
		return node, ErrEndpointInvalid
	}
	node.Name, node.Endpoint = name, endpoint
	node.Group, node.Tags = strings.TrimSpace(input.Group), strings.TrimSpace(input.Tags)
	if input.Enabled != nil {
		node.Enabled = *input.Enabled
		if !node.Enabled {
			node.Status = models.ClusterNodeStatusOffline
		}
	}
	if err := m.db.Save(&node).Error; err != nil {
		return node, err
	}
	return node, nil
}

func (m *Manager) DeleteNode(id uint) error {
	return m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("node_id = ?", id).Delete(&models.ClusterNodeMetric{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&models.ClusterTask{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&models.ClusterNode{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNodeNotFound
		}
		return nil
	})
}

// RotateToken invalidates the previous agent token and returns a new one. The
// plaintext token is only available in this response and is never persisted.
func (m *Manager) RotateToken(id uint) (CreateNodeResult, error) {
	node, err := m.GetNode(id)
	if err != nil {
		return CreateNodeResult{}, err
	}
	token, err := generateToken()
	if err != nil {
		return CreateNodeResult{}, err
	}
	node.TokenHash = hashToken(token)
	node.Status = models.ClusterNodeStatusPending
	node.LastSeenAt = nil
	if err := m.db.Save(&node).Error; err != nil {
		return CreateNodeResult{}, err
	}
	return CreateNodeResult{Node: node, Token: token}, nil
}

func (m *Manager) RegisterNode(input NodeRegistration) (models.ClusterNode, error) {
	node, err := m.findByToken(input.Token)
	if err != nil {
		return node, err
	}
	if !node.Enabled {
		return node, ErrNodeDisabled
	}
	now := time.Now()
	node.Hostname, node.SystemID, node.SystemVersion = strings.TrimSpace(input.Hostname), strings.TrimSpace(input.SystemID), strings.TrimSpace(input.SystemVersion)
	node.Architecture, node.PanelVersion, node.AgentVersion = strings.TrimSpace(input.Architecture), strings.TrimSpace(input.PanelVersion), strings.TrimSpace(input.AgentVersion)
	node.Capabilities = normalizeCapabilities(input.Capabilities)
	node.HeartbeatIntervalSeconds = normalizeHeartbeatInterval(input.HeartbeatIntervalSeconds)
	node.Status, node.LastError, node.LastSeenAt, node.LastRegisteredAt, node.DepartedAt = models.ClusterNodeStatusOnline, "", &now, &now, nil
	applyHostSnapshot(&node, input.HostSnapshot)
	if err := m.db.Save(&node).Error; err != nil {
		return node, err
	}
	return node, nil
}

func (m *Manager) Heartbeat(input NodeHeartbeat) (models.ClusterNode, error) {
	node, err := m.findByToken(input.Token)
	if err != nil {
		return node, err
	}
	if !node.Enabled {
		return node, ErrNodeDisabled
	}
	if node.DepartedAt != nil {
		return node, ErrNodeDeparted
	}
	now := time.Now()
	node.Hostname, node.PanelVersion, node.AgentVersion = strings.TrimSpace(input.Hostname), strings.TrimSpace(input.PanelVersion), strings.TrimSpace(input.AgentVersion)
	node.Capabilities = normalizeCapabilities(input.Capabilities)
	node.HeartbeatIntervalSeconds = normalizeHeartbeatInterval(input.HeartbeatIntervalSeconds)
	node.CPUPercent, node.MemoryPercent, node.DiskPercent = clamp(input.CPUPercent), clamp(input.MemoryPercent), clamp(input.DiskPercent)
	node.NetworkRecvBPS, node.NetworkSendBPS, node.UptimeSeconds = max0(input.NetworkRecvBPS), max0(input.NetworkSendBPS), input.UptimeSeconds
	node.CPUTotalCores, node.CPUUsedCores = input.CPUTotalCores, max0(input.CPUUsedCores)
	node.MemoryUsedBytes, node.MemoryTotalBytes = input.MemoryUsedBytes, input.MemoryTotalBytes
	node.DiskUsedBytes, node.DiskTotalBytes = input.DiskUsedBytes, input.DiskTotalBytes
	node.IPAddress, node.SubnetMask, node.Gateway = strings.TrimSpace(input.IPAddress), strings.TrimSpace(input.SubnetMask), strings.TrimSpace(input.Gateway)
	node.MACAddress, node.InterfaceName = strings.TrimSpace(input.MACAddress), strings.TrimSpace(input.InterfaceName)
	node.Status, node.LastError, node.LastSeenAt = models.ClusterNodeStatusOnline, "", &now
	metric := models.ClusterNodeMetric{NodeID: node.ID, CapturedAt: now, CPUPercent: node.CPUPercent, MemoryPercent: node.MemoryPercent, DiskPercent: node.DiskPercent, NetworkRecvBPS: node.NetworkRecvBPS, NetworkSendBPS: node.NetworkSendBPS, UptimeSeconds: node.UptimeSeconds}
	if err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(&node).Error; err != nil {
			return err
		}
		return tx.Create(&metric).Error
	}); err != nil {
		return node, err
	}
	return node, nil
}

// MarkOffline handles a graceful agent departure. Unexpected failures still
// fall back to the heartbeat expiry supervisor.
func (m *Manager) MarkOffline(token string) (models.ClusterNode, error) {
	node, err := m.findByToken(token)
	if err != nil {
		return node, err
	}
	now := time.Now()
	if err := m.db.Model(&models.ClusterNode{}).Where("id = ?", node.ID).Updates(map[string]interface{}{
		"status":      models.ClusterNodeStatusOffline,
		"last_error":  "",
		"departed_at": now,
	}).Error; err != nil {
		return node, err
	}
	node.Status, node.LastError, node.DepartedAt = models.ClusterNodeStatusOffline, "", &now
	return node, nil
}

func nodeHeartbeatFresh(node models.ClusterNode, now time.Time) bool {
	if !node.Enabled || node.Status != models.ClusterNodeStatusOnline || node.LastSeenAt == nil {
		return false
	}
	interval := time.Duration(normalizeHeartbeatInterval(node.HeartbeatIntervalSeconds)) * time.Second
	staleAfter := 3 * interval
	if staleAfter < time.Minute {
		staleAfter = time.Minute
	}
	return !node.LastSeenAt.Before(now.Add(-staleAfter))
}

func normalizeHeartbeatInterval(interval int) int {
	if interval < 5 || interval > 3600 {
		return defaultAgentIntervalSeconds
	}
	return interval
}

func normalizeCapabilities(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 120 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) == 64 {
			break
		}
	}
	return result
}

func nodeHasCapability(node models.ClusterNode, capability string) bool {
	for _, value := range node.Capabilities {
		if value == capability {
			return true
		}
	}
	return false
}

func applyHostSnapshot(node *models.ClusterNode, snapshot HostSnapshot) {
	node.CPUPercent, node.MemoryPercent, node.DiskPercent = clamp(snapshot.CPUPercent), clamp(snapshot.MemoryPercent), clamp(snapshot.DiskPercent)
	node.NetworkRecvBPS, node.NetworkSendBPS, node.UptimeSeconds = max0(snapshot.NetworkRecvBPS), max0(snapshot.NetworkSendBPS), snapshot.UptimeSeconds
	node.CPUTotalCores, node.CPUUsedCores = snapshot.CPUTotalCores, max0(snapshot.CPUUsedCores)
	node.MemoryUsedBytes, node.MemoryTotalBytes = snapshot.MemoryUsedBytes, snapshot.MemoryTotalBytes
	node.DiskUsedBytes, node.DiskTotalBytes = snapshot.DiskUsedBytes, snapshot.DiskTotalBytes
	node.IPAddress, node.SubnetMask, node.Gateway = strings.TrimSpace(snapshot.IPAddress), strings.TrimSpace(snapshot.SubnetMask), strings.TrimSpace(snapshot.Gateway)
	node.MACAddress, node.InterfaceName = strings.TrimSpace(snapshot.MACAddress), strings.TrimSpace(snapshot.InterfaceName)
}

func (m *Manager) findByToken(token string) (models.ClusterNode, error) {
	var node models.ClusterNode
	if strings.TrimSpace(token) == "" {
		return node, ErrInvalidToken
	}
	if err := m.db.Where("token_hash = ?", hashToken(token)).First(&node).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return node, ErrInvalidToken
		}
		return node, err
	}
	return node, nil
}

func validEndpoint(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	if port := u.Port(); port != "" {
		parsedPort, err := strconv.Atoi(port)
		if err != nil || parsedPort < 1 || parsedPort > 65535 {
			return false
		}
	}
	return true
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate node token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum[:])
}
func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
func max0(v float64) float64 {
	if v < 0 {
		return 0
	}
	return v
}
