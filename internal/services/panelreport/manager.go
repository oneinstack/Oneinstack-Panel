package panelreport

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"oneinstack/internal/buildinfo"
	"oneinstack/internal/models"
	"oneinstack/internal/services/translation"

	"gorm.io/gorm"
)

const (
	signatureDomain       = "oneinstack-center-operations-v1"
	syncPath              = "/v1/panel/operations/sync"
	maxRawLogBytes  int64 = 20 << 20
	logHalfBytes    int64 = 10 << 20
)

type Config struct {
	BaseURL, InstallDir, IdentityPath, ActivationCodeFile, LogDir string
	Timeout                                                       time.Duration
}

type Manager struct {
	db            *gorm.DB
	config        Config
	cancel        context.CancelFunc
	done          chan struct{}
	mu            sync.Mutex
	lastInventory time.Time
	nextAttempt   time.Time
	retryDelay    time.Duration
}

type serverSnapshot struct {
	HostFingerprint string    `json:"hostFingerprint,omitempty"`
	OSID            string    `json:"osId,omitempty"`
	OSVersion       string    `json:"osVersion,omitempty"`
	KernelVersion   string    `json:"kernelVersion,omitempty"`
	Architecture    string    `json:"architecture,omitempty"`
	Virtualization  string    `json:"virtualization,omitempty"`
	PanelVersion    string    `json:"panelVersion,omitempty"`
	UpdatedAt       time.Time `json:"updatedAt"`
}
type componentReport struct {
	Component        string     `json:"component"`
	SoftwareKey      string     `json:"softwareKey,omitempty"`
	Name             string     `json:"name,omitempty"`
	SoftwareVersion  string     `json:"softwareVersion,omitempty"`
	PackageVersion   string     `json:"packageVersion,omitempty"`
	Installed        bool       `json:"installed"`
	InstalledAt      *time.Time `json:"installedAt,omitempty"`
	UninstalledAt    *time.Time `json:"uninstalledAt,omitempty"`
	LatestOperation  string     `json:"latestOperation,omitempty"`
	LatestStatus     string     `json:"latestStatus,omitempty"`
	LatestOperatedAt *time.Time `json:"latestOperatedAt,omitempty"`
}
type operationReport struct {
	TaskID           string     `json:"taskId"`
	Operation        string     `json:"operation"`
	Component        string     `json:"component"`
	SoftwareKey      string     `json:"softwareKey,omitempty"`
	Name             string     `json:"name,omitempty"`
	RequestedVersion string     `json:"requestedVersion,omitempty"`
	ResolvedVersion  string     `json:"resolvedVersion,omitempty"`
	RuntimeVersion   string     `json:"runtimeVersion,omitempty"`
	PackageVersion   string     `json:"packageVersion,omitempty"`
	Status           string     `json:"status"`
	FailurePhase     string     `json:"failurePhase,omitempty"`
	ErrorCode        string     `json:"errorCode,omitempty"`
	ErrorMessage     string     `json:"errorMessage,omitempty"`
	RollbackStatus   string     `json:"rollbackStatus,omitempty"`
	StartedAt        *time.Time `json:"startedAt,omitempty"`
	FinishedAt       *time.Time `json:"finishedAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	LogStatus        string     `json:"logStatus,omitempty"`
}
type syncReport struct {
	SchemaVersion     int               `json:"schemaVersion"`
	InstanceID        string            `json:"instanceId"`
	Server            serverSnapshot    `json:"server"`
	InventoryComplete bool              `json:"inventoryComplete,omitempty"`
	Components        []componentReport `json:"components,omitempty"`
	Operations        []operationReport `json:"operations,omitempty"`
}

func New(db *gorm.DB, config Config) (*Manager, error) {
	if db == nil {
		return nil, errors.New("Panel operation reporter database is required")
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.InstallDir = filepath.Clean(config.InstallDir)
	config.LogDir = filepath.Clean(config.LogDir)
	if config.BaseURL == "" || config.InstallDir == "" || config.LogDir == "" {
		return nil, errors.New("Panel operation reporter configuration is incomplete")
	}
	if config.Timeout <= 0 {
		config.Timeout = 20 * time.Second
	}
	return &Manager{db: db, config: config, done: make(chan struct{})}, nil
}

func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	go m.run(ctx)
}
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	client, err := translation.NewSignedPanelClient(ctx, translation.SignedPanelClientConfig{BaseURL: m.config.BaseURL, InstallDir: m.config.InstallDir, IdentityPath: m.config.IdentityPath, ActivationCodeFile: m.config.ActivationCodeFile, Timeout: m.config.Timeout})
	if err != nil {
		return
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			m.syncOnce(ctx, client)
			timer.Reset(30 * time.Second)
		}
	}
}

func (m *Manager) syncOnce(ctx context.Context, client *translation.SignedPanelClient) {
	now := time.Now().UTC()
	m.mu.Lock()
	if !m.nextAttempt.IsZero() && now.Before(m.nextAttempt) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	var tasks []models.SoftwareTask
	terminal := []string{models.SoftwareTaskStatusSucceeded, models.SoftwareTaskStatusFailed, models.SoftwareTaskStatusCanceled, models.SoftwareTaskStatusInterrupted}
	if err := m.db.Where("status IN ? AND center_reported_at IS NULL AND (center_report_next_at IS NULL OR center_report_next_at <= ?)", terminal, now).Order("created_at ASC").Limit(100).Find(&tasks).Error; err != nil {
		return
	}
	includeInventory := m.lastInventory.IsZero() || now.Sub(m.lastInventory) >= 24*time.Hour
	if len(tasks) == 0 && !includeInventory {
		m.uploadPendingLogs(ctx, client)
		return
	}
	software := m.loadSoftware()
	names := softwareNames(software)
	report := syncReport{SchemaVersion: 1, InstanceID: client.InstanceID(), Server: collectServerSnapshot(ctx)}
	if includeInventory {
		report.InventoryComplete = true
		report.Components = installedComponents(software)
	}
	for i := range tasks {
		report.Operations = append(report.Operations, operationFromTask(tasks[i], names, m.logAvailable(tasks[i])))
	}
	body, err := json.Marshal(report)
	if err != nil {
		return
	}
	request, err := client.NewRequest(ctx, http.MethodPost, syncPath, signatureDomain, body)
	if err != nil {
		m.markReportFailure(tasks, err)
		m.scheduleRetry(now)
		return
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		m.markReportFailure(tasks, err)
		m.scheduleRetry(now)
		return
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		m.markReportFailure(tasks, fmt.Errorf("Center returned HTTP %d", response.StatusCode))
		m.scheduleRetry(now)
		return
	}
	m.clearRetry()
	if len(tasks) > 0 {
		ids := make([]string, 0, len(tasks))
		for _, task := range tasks {
			ids = append(ids, task.ID)
		}
		m.db.Model(&models.SoftwareTask{}).Where("id IN ?", ids).Updates(map[string]any{"center_reported_at": now, "center_report_attempts": 0, "center_report_error": "", "center_report_next_at": nil})
	}
	if includeInventory {
		m.lastInventory = now
	}
	m.uploadPendingLogs(ctx, client)
}

func (m *Manager) scheduleRetry(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.retryDelay < time.Minute {
		m.retryDelay = time.Minute
	} else {
		m.retryDelay *= 2
	}
	if m.retryDelay > 6*time.Hour {
		m.retryDelay = 6 * time.Hour
	}
	jitter := time.Duration((now.UnixNano() >> 8) % int64(30*time.Second))
	m.nextAttempt = now.Add(m.retryDelay + jitter)
}

func (m *Manager) clearRetry() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextAttempt = time.Time{}
	m.retryDelay = 0
}

func (m *Manager) uploadPendingLogs(ctx context.Context, client *translation.SignedPanelClient) {
	var tasks []models.SoftwareTask
	if err := m.db.Where("status=? AND center_reported_at IS NOT NULL AND center_log_reported_at IS NULL AND (center_report_next_at IS NULL OR center_report_next_at<=?)", models.SoftwareTaskStatusFailed, time.Now()).Order("finished_at ASC").Limit(10).Find(&tasks).Error; err != nil {
		return
	}
	for i := range tasks {
		task := &tasks[i]
		payload, original, truncated, err := m.prepareLog(*task)
		if errors.Is(err, os.ErrNotExist) {
			now := time.Now()
			m.db.Model(task).Updates(map[string]any{"center_log_reported_at": now, "center_report_attempts": 0, "center_report_error": "", "center_report_next_at": nil})
			continue
		}
		if err != nil {
			continue
		}
		path := "/v1/panel/operations/" + task.ID + "/log"
		request, err := client.NewRequest(ctx, http.MethodPut, path, signatureDomain, payload)
		if err != nil {
			m.markReportFailure([]models.SoftwareTask{*task}, err)
			m.scheduleRetry(time.Now())
			return
		}
		digest := sha256.Sum256(payload)
		request.Header.Set("Content-Type", "application/gzip")
		request.Header.Set("X-Oneinstack-Log-SHA256", hex.EncodeToString(digest[:]))
		request.Header.Set("X-Oneinstack-Log-Original-Bytes", strconv.FormatInt(original, 10))
		request.Header.Set("X-Oneinstack-Log-Truncated", strconv.FormatBool(truncated))
		response, err := client.Do(request)
		if err != nil {
			m.markReportFailure([]models.SoftwareTask{*task}, err)
			m.scheduleRetry(time.Now())
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			m.markReportFailure([]models.SoftwareTask{*task}, fmt.Errorf("Center returned HTTP %d", response.StatusCode))
			m.scheduleRetry(time.Now())
			return
		}
		m.clearRetry()
		now := time.Now()
		m.db.Model(task).Updates(map[string]any{"center_log_reported_at": now, "center_report_attempts": 0, "center_report_error": "", "center_report_next_at": nil})
	}
}

func (m *Manager) markReportFailure(tasks []models.SoftwareTask, cause error) {
	if len(tasks) == 0 {
		return
	}
	now := time.Now()
	for i := range tasks {
		attempts := tasks[i].CenterReportAttempts + 1
		delay := time.Duration(1<<min(attempts, 10)) * 30 * time.Second
		if delay > 6*time.Hour {
			delay = 6 * time.Hour
		}
		jitter := time.Duration((int(tasks[i].ID[0])+attempts*17)%30) * time.Second
		next := now.Add(delay + jitter)
		message := cause.Error()
		if len(message) > 512 {
			message = message[:512]
		}
		m.db.Model(&models.SoftwareTask{}).Where("id=?", tasks[i].ID).Updates(map[string]any{"center_report_attempts": attempts, "center_report_error": message, "center_report_next_at": next})
	}
}

func (m *Manager) loadSoftware() []models.Software {
	var rows []models.Software
	_ = m.db.Order("installed DESC,id DESC").Find(&rows).Error
	return rows
}

type softwareInfo struct{ Name, PackageVersion string }

func softwareNames(rows []models.Software) map[string]softwareInfo {
	result := map[string]softwareInfo{}
	for _, row := range rows {
		for _, key := range []string{strings.ToLower(strings.TrimSpace(row.Component)), strings.ToLower(strings.TrimSpace(row.Key))} {
			if key == "" {
				continue
			}
			if _, exists := result[key]; !exists {
				result[key] = softwareInfo{Name: row.Name, PackageVersion: row.InstalledPackageVersion}
			}
		}
	}
	return result
}
func installedComponents(rows []models.Software) []componentReport {
	result := []componentReport{}
	seen := map[string]bool{}
	for _, row := range rows {
		if !row.Installed {
			continue
		}
		component := strings.ToLower(strings.TrimSpace(row.Component))
		if component == "" {
			component = strings.ToLower(strings.TrimSpace(row.Key))
		}
		if component == "" || seen[component] {
			continue
		}
		seen[component] = true
		version := strings.TrimSpace(row.RuntimeVersion)
		if version == "" {
			version = strings.TrimSpace(row.InstallVersion)
		}
		if version == "" {
			version = strings.TrimSpace(row.Version)
		}
		installedAt := row.InstallTime
		if installedAt.IsZero() {
			installedAt = row.CreateTime
		}
		result = append(result, componentReport{Component: component, SoftwareKey: row.Key, Name: row.Name, SoftwareVersion: version, PackageVersion: row.InstalledPackageVersion, Installed: true, InstalledAt: &installedAt})
	}
	return result
}
func operationFromTask(task models.SoftwareTask, names map[string]softwareInfo, logAvailable bool) operationReport {
	info := names[strings.ToLower(strings.TrimSpace(task.Component))]
	status := ""
	if task.Status == models.SoftwareTaskStatusFailed {
		if logAvailable {
			status = "pending"
		} else {
			status = "unavailable"
		}
	}
	return operationReport{TaskID: task.ID, Operation: task.Operation, Component: task.Component, SoftwareKey: task.SoftwareKey, Name: info.Name, RequestedVersion: task.RequestedVersion, ResolvedVersion: task.ResolvedVersion, RuntimeVersion: task.RuntimeVersion, PackageVersion: info.PackageVersion, Status: task.Status, FailurePhase: task.FailurePhase, ErrorCode: task.ErrorCode, ErrorMessage: task.ErrorMessage, RollbackStatus: task.RollbackStatus, StartedAt: task.StartedAt, FinishedAt: task.FinishedAt, CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, LogStatus: status}
}

func (m *Manager) logAvailable(task models.SoftwareTask) bool {
	path, err := m.safeLogPath(task)
	if err != nil {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}
func (m *Manager) safeLogPath(task models.SoftwareTask) (string, error) {
	expected := "task_" + task.ID + ".log"
	if filepath.Base(task.LogPath) != expected {
		return "", os.ErrNotExist
	}
	root, err := filepath.Abs(m.config.LogDir)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Clean(task.LogPath))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative != expected {
		return "", os.ErrNotExist
	}
	return path, nil
}
func (m *Manager) prepareLog(task models.SoftwareTask) ([]byte, int64, bool, error) {
	path, err := m.safeLogPath(task)
	if err != nil {
		return nil, 0, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, false, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, false, os.ErrNotExist
	}
	size := info.Size()
	truncated := size > maxRawLogBytes
	var raw []byte
	if !truncated {
		raw, err = io.ReadAll(io.LimitReader(file, maxRawLogBytes+1))
	} else {
		raw = make([]byte, 0, maxRawLogBytes+64)
		head := make([]byte, logHalfBytes)
		n, readErr := io.ReadFull(file, head)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, 0, false, readErr
		}
		raw = append(raw, head[:n]...)
		raw = append(raw, []byte("\n[... Panel log truncated before Center upload ...]\n")...)
		tail := make([]byte, logHalfBytes)
		if _, err = file.ReadAt(tail, size-logHalfBytes); err != nil && err != io.EOF {
			return nil, 0, false, err
		}
		raw = append(raw, tail...)
	}
	if err != nil {
		return nil, 0, false, err
	}
	raw = redactLog(raw)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(raw); err != nil {
		return nil, 0, false, err
	}
	if err := writer.Close(); err != nil {
		return nil, 0, false, err
	}
	return compressed.Bytes(), size, truncated, nil
}

var secretPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|token|secret|authorization|cookie|api[_-]?key)([[:space:]]*[:=][[:space:]]*)([^[:space:]]+)`)
var privateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [^-\n]*PRIVATE KEY-----.*?-----END [^-\n]*PRIVATE KEY-----`)

func redactLog(value []byte) []byte {
	value = secretPattern.ReplaceAll(value, []byte("$1$2[REDACTED]"))
	return privateKeyPattern.ReplaceAll(value, []byte("[REDACTED PRIVATE KEY]"))
}

func collectServerSnapshot(ctx context.Context) serverSnapshot {
	hostname, _ := os.Hostname()
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(hostname))))
	osID, osVersion := readOSRelease()
	kernel := commandOutput(ctx, "uname", "-r")
	virtualization := commandOutput(ctx, "systemd-detect-virt")
	if virtualization == "" {
		virtualization = "unknown"
	}
	return serverSnapshot{HostFingerprint: hex.EncodeToString(digest[:8]), OSID: osID, OSVersion: osVersion, KernelVersion: kernel, Architecture: runtime.GOARCH, Virtualization: virtualization, PanelVersion: buildinfo.Version, UpdatedAt: time.Now().UTC()}
}
func readOSRelease() (string, string) {
	contents, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS, ""
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = strings.Trim(strings.TrimSpace(value), "\"")
		}
	}
	return values["ID"], values["VERSION_ID"]
}
func commandOutput(parent context.Context, name string, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
