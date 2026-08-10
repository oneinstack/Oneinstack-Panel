package system

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/services/safe"
	panelServer "oneinstack/server"

	"golang.org/x/sys/unix"
)

const (
	panelServiceUnit              = "one.service"
	networkApplyStatusScheduled   = "scheduled"
	networkApplyStatusApplying    = "applying"
	networkApplyStatusSucceeded   = "succeeded"
	networkApplyStatusRolledBack  = "rolled_back"
	networkApplyStatusFailed      = "failed"
	networkTransactionMaxBytes    = 256 * 1024
	networkConfigSnapshotMaxBytes = 4 * 1024 * 1024
	networkApplyDelay             = 2 * time.Second
	networkApplyTimeout           = 45 * time.Second
	networkTransactionRetention   = 20
)

var (
	ErrNetworkTransactionNotFound = errors.New("panel network transaction not found")
	ErrNetworkApplyInProgress     = errors.New("panel network transaction is already in progress")
)

type PanelNetworkTransactionStatus struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	HTTPURL     string     `json:"httpUrl"`
	HTTPSURL    string     `json:"httpsUrl,omitempty"`
	RolledBack  bool       `json:"rolledBack"`
	Recoverable bool       `json:"recoverable"`
}

type panelNetworkTransaction struct {
	ID               string                  `json:"id"`
	Status           string                  `json:"status"`
	Error            string                  `json:"error,omitempty"`
	CreatedAt        time.Time               `json:"createdAt"`
	NotBefore        time.Time               `json:"notBefore"`
	StartedAt        *time.Time              `json:"startedAt,omitempty"`
	FinishedAt       *time.Time              `json:"finishedAt,omitempty"`
	Previous         panelServer.PanelConfig `json:"previous"`
	Candidate        panelServer.PanelConfig `json:"candidate"`
	PreviousSHA256   string                  `json:"previousSha256"`
	CandidateSHA256  string                  `json:"candidateSha256"`
	PreparedRuleIDs  []int64                 `json:"preparedRuleIds,omitempty"`
	PreviousFileName string                  `json:"previousFileName"`
}

func panelNetworkAutoApplySupported(ctx context.Context) bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return false
	}
	checkContext, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(
		checkContext,
		"systemctl",
		"show",
		panelServiceUnit,
		"--property=LoadState",
		"--value",
	).Output()
	return err == nil && strings.TrimSpace(string(output)) == "loaded"
}

func createPanelNetworkTransaction(
	previous panelServer.PanelConfig,
	candidate panelServer.PanelConfig,
	previousConfig []byte,
	candidateConfig []byte,
	rules []preparedPanelRule,
) (*panelNetworkTransaction, error) {
	id, err := randomNetworkTransactionID()
	if err != nil {
		return nil, err
	}
	directory := panelNetworkTransactionDirectory()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("创建访问配置事务目录: %w", err)
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, fmt.Errorf("保护访问配置事务目录: %w", err)
	}
	previousFileName := id + ".previous.yaml"
	previousPath := filepath.Join(directory, previousFileName)
	if err := writeExclusiveFile(previousPath, previousConfig, 0600); err != nil {
		return nil, fmt.Errorf("保存访问配置恢复快照: %w", err)
	}
	preparedRuleIDs := make([]int64, 0, len(rules))
	for _, rule := range rules {
		if rule.created && rule.id > 0 {
			preparedRuleIDs = append(preparedRuleIDs, rule.id)
		}
	}
	now := time.Now().UTC()
	transaction := &panelNetworkTransaction{
		ID:               id,
		Status:           networkApplyStatusScheduled,
		CreatedAt:        now,
		NotBefore:        now.Add(networkApplyDelay),
		Previous:         previous,
		Candidate:        candidate,
		PreviousSHA256:   checksumHex(previousConfig),
		CandidateSHA256:  checksumHex(candidateConfig),
		PreparedRuleIDs:  preparedRuleIDs,
		PreviousFileName: previousFileName,
	}
	if err := savePanelNetworkTransaction(transaction); err != nil {
		_ = os.Remove(previousPath)
		return nil, err
	}
	prunePanelNetworkTransactions(directory, networkTransactionRetention)
	return transaction, nil
}

func schedulePanelNetworkTransaction(ctx context.Context, transactionID string) error {
	if !validNetworkTransactionID(transactionID) {
		return errors.New("invalid panel network transaction id")
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("定位 Panel 可执行文件: %w", err)
	}
	basePath := app.GetBasePath()
	configPath := configFilePath()
	unitName := "one-network-apply-" + transactionID
	command := exec.CommandContext(
		ctx,
		"systemd-run",
		"--no-block",
		"--unit="+unitName,
		"--collect",
		"--property=Type=oneshot",
		"--property=TimeoutStartSec=2min",
		"--property=UMask=0077",
		"--setenv=ONEINSTACK_BASE_PATH="+basePath,
		"--setenv=ONEINSTACK_CONFIG_PATH="+configPath,
		executable,
		"network",
		"apply",
		"--transaction="+transactionID,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"创建访问配置应用任务: %w: %s",
			err,
			truncateNetworkTransactionError(string(output)),
		)
	}
	return nil
}

func ApplyPanelNetworkTransaction(ctx context.Context, transactionID string) error {
	return withPanelNetworkTransactionLock(ctx, func() error {
		return applyPanelNetworkTransaction(ctx, transactionID)
	})
}

func applyPanelNetworkTransaction(ctx context.Context, transactionID string) error {
	transaction, err := loadPanelNetworkTransaction(transactionID)
	if err != nil {
		return err
	}
	if transaction.Status == networkApplyStatusSucceeded ||
		transaction.Status == networkApplyStatusRolledBack {
		return nil
	}
	if transaction.Status != networkApplyStatusScheduled &&
		transaction.Status != networkApplyStatusApplying {
		return fmt.Errorf("panel network transaction is in terminal state %s", transaction.Status)
	}
	currentConfig, err := readLimitedFile(configFilePath(), networkConfigSnapshotMaxBytes)
	if err != nil || checksumHex(currentConfig) != transaction.CandidateSHA256 {
		finished := time.Now().UTC()
		transaction.Status = networkApplyStatusFailed
		transaction.FinishedAt = &finished
		transaction.Error = "待应用配置已被其他操作替换，事务已停止"
		_ = savePanelNetworkTransaction(transaction)
		return errors.New(transaction.Error)
	}
	if delay := time.Until(transaction.NotBefore); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	now := time.Now().UTC()
	transaction.Status = networkApplyStatusApplying
	transaction.StartedAt = &now
	transaction.Error = ""
	if err := savePanelNetworkTransaction(transaction); err != nil {
		return err
	}

	applyErr := restartPanelService(ctx)
	if applyErr == nil {
		applyErr = waitForPanelReady(ctx, transaction.Candidate, networkApplyTimeout)
	}
	if applyErr == nil {
		currentConfig, readErr := readLimitedFile(configFilePath(), networkConfigSnapshotMaxBytes)
		switch {
		case readErr != nil:
			applyErr = fmt.Errorf("重启后读取待应用配置: %w", readErr)
		case checksumHex(currentConfig) != transaction.CandidateSHA256:
			applyErr = errors.New("重启后待应用配置已被其他操作替换")
		}
	}
	if applyErr == nil {
		finished := time.Now().UTC()
		transaction.Status = networkApplyStatusSucceeded
		transaction.FinishedAt = &finished
		transaction.Error = ""
		if err := savePanelNetworkTransaction(transaction); err != nil {
			return err
		}
		_ = removePanelNetworkSnapshot(transaction)
		return nil
	}

	return rollbackPanelNetworkTransaction(
		ctx,
		transaction,
		fmt.Errorf("新访问配置启动或健康检查失败: %w", applyErr),
		true,
	)
}

func RecoverPendingPanelNetworkTransaction(ctx context.Context) error {
	return withPanelNetworkTransactionLock(ctx, func() error {
		return recoverPendingPanelNetworkTransaction(ctx)
	})
}

func recoverPendingPanelNetworkTransaction(ctx context.Context) error {
	transaction, err := latestPendingPanelNetworkTransaction()
	if errors.Is(err, ErrNetworkTransactionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	reason := errors.New("systemd 检测到 Panel 使用新访问配置启动失败")
	return rollbackPanelNetworkTransaction(ctx, transaction, reason, true)
}

func FinalizePendingPanelNetworkTransaction() error {
	ctx, cancel := context.WithTimeout(context.Background(), networkApplyTimeout)
	defer cancel()
	return withPanelNetworkTransactionLock(ctx, finalizePendingPanelNetworkTransaction)
}

func finalizePendingPanelNetworkTransaction() error {
	transaction, err := latestPendingPanelNetworkTransaction()
	if errors.Is(err, ErrNetworkTransactionNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	currentConfig, err := readLimitedFile(configFilePath(), networkConfigSnapshotMaxBytes)
	if err != nil {
		return err
	}
	if checksumHex(currentConfig) != transaction.CandidateSHA256 {
		return nil
	}
	finished := time.Now().UTC()
	transaction.Status = networkApplyStatusSucceeded
	transaction.FinishedAt = &finished
	transaction.Error = ""
	if err := savePanelNetworkTransaction(transaction); err != nil {
		return err
	}
	return removePanelNetworkSnapshot(transaction)
}

func rollbackPanelNetworkTransaction(
	ctx context.Context,
	transaction *panelNetworkTransaction,
	reason error,
	restart bool,
) error {
	restoreErr := restorePanelNetworkSnapshot(transaction)
	firewallErr := rollbackPanelNetworkTransactionRules(ctx, transaction)
	var restartErr error
	if restart {
		_ = exec.CommandContext(ctx, "systemctl", "stop", panelServiceUnit).Run()
		_ = exec.CommandContext(ctx, "systemctl", "reset-failed", panelServiceUnit).Run()
		restartErr = startPanelService(ctx)
		if restartErr == nil {
			restartErr = waitForPanelReady(ctx, transaction.Previous, networkApplyTimeout)
		}
	}
	finished := time.Now().UTC()
	transaction.FinishedAt = &finished
	transaction.Error = truncateNetworkTransactionError(
		errors.Join(reason, restoreErr, firewallErr, restartErr).Error(),
	)
	if restoreErr == nil && restartErr == nil {
		transaction.Status = networkApplyStatusRolledBack
	} else {
		transaction.Status = networkApplyStatusFailed
	}
	saveErr := savePanelNetworkTransaction(transaction)
	if transaction.Status == networkApplyStatusRolledBack {
		_ = removePanelNetworkSnapshot(transaction)
	}
	if saveErr != nil {
		return errors.Join(reason, restoreErr, firewallErr, restartErr, saveErr)
	}
	if transaction.Status == networkApplyStatusFailed {
		return errors.Join(reason, restoreErr, firewallErr, restartErr)
	}
	return nil
}

func GetPanelNetworkTransaction(transactionID string) (*PanelNetworkTransactionStatus, error) {
	transaction, err := loadPanelNetworkTransaction(transactionID)
	if err != nil {
		return nil, err
	}
	return describePanelNetworkTransaction(transaction), nil
}

func latestPanelNetworkTransactionStatus() *PanelNetworkTransactionStatus {
	transaction, err := latestPanelNetworkTransaction()
	if err != nil {
		return nil
	}
	return describePanelNetworkTransaction(transaction)
}

func describePanelNetworkTransaction(
	transaction *panelNetworkTransaction,
) *PanelNetworkTransactionStatus {
	status := &PanelNetworkTransactionStatus{
		ID:         transaction.ID,
		Status:     transaction.Status,
		Error:      transaction.Error,
		CreatedAt:  transaction.CreatedAt,
		StartedAt:  transaction.StartedAt,
		FinishedAt: transaction.FinishedAt,
		HTTPURL:    accessURL("http", transaction.Candidate.BindAddress, transaction.Candidate.HTTPPort),
		RolledBack: transaction.Status == networkApplyStatusRolledBack,
		Recoverable: transaction.Status == networkApplyStatusScheduled ||
			transaction.Status == networkApplyStatusApplying ||
			transaction.Status == networkApplyStatusFailed,
	}
	if transaction.Candidate.HTTPSEnabled {
		status.HTTPSURL = accessURL(
			"https",
			transaction.Candidate.BindAddress,
			transaction.Candidate.HTTPSPort,
		)
	}
	return status
}

func restartPanelService(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "systemctl", "restart", panelServiceUnit).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"systemctl restart %s: %w: %s",
			panelServiceUnit,
			err,
			truncateNetworkTransactionError(string(output)),
		)
	}
	return nil
}

func startPanelService(ctx context.Context) error {
	output, err := exec.CommandContext(ctx, "systemctl", "start", "--no-block", panelServiceUnit).CombinedOutput()
	if err != nil {
		return fmt.Errorf(
			"systemctl start %s: %w: %s",
			panelServiceUnit,
			err,
			truncateNetworkTransactionError(string(output)),
		)
	}
	return nil
}

func waitForPanelReady(
	ctx context.Context,
	config panelServer.PanelConfig,
	timeout time.Duration,
) error {
	healthURL := localPanelHealthURL(config)
	deadline := time.Now().Add(timeout)
	client := &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second,
				KeepAlive: -1,
			}).DialContext,
			DisableKeepAlives: true,
		},
	}
	defer client.CloseIdleConnections()
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		activeContext, cancel := context.WithTimeout(ctx, 2*time.Second)
		activeErr := exec.CommandContext(
			activeContext,
			"systemctl",
			"is-active",
			"--quiet",
			panelServiceUnit,
		).Run()
		cancel()
		if activeErr == nil {
			request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
			if requestErr == nil {
				response, responseErr := client.Do(request)
				if responseErr == nil {
					_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
					_ = response.Body.Close()
					if response.StatusCode == http.StatusOK {
						return nil
					}
					lastErr = fmt.Errorf("就绪探针返回 HTTP %d", response.StatusCode)
				} else {
					lastErr = responseErr
				}
			} else {
				lastErr = requestErr
			}
		} else {
			lastErr = activeErr
		}
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	if lastErr == nil {
		lastErr = errors.New("就绪检查超时")
	}
	return fmt.Errorf("Panel 未在 %s 内就绪: %w", timeout, lastErr)
}

func localPanelHealthURL(config panelServer.PanelConfig) string {
	host := strings.TrimSpace(config.BindAddress)
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return "http://" + net.JoinHostPort(host, config.HTTPPort) + "/health/ready"
}

func rollbackPanelNetworkTransactionRules(
	ctx context.Context,
	transaction *panelNetworkTransaction,
) error {
	if len(transaction.PreparedRuleIDs) == 0 {
		return nil
	}
	if err := app.Initialize(); err != nil {
		return fmt.Errorf("恢复旧配置后初始化防火墙补偿环境: %w", err)
	}
	service := safe.NewDefaultService()
	var rollbackErr error
	for index := len(transaction.PreparedRuleIDs) - 1; index >= 0; index-- {
		if err := service.RollbackPreparedPanelPort(ctx, transaction.PreparedRuleIDs[index]); err != nil {
			rollbackErr = errors.Join(rollbackErr, err)
		}
	}
	return rollbackErr
}

func restorePanelNetworkSnapshot(transaction *panelNetworkTransaction) error {
	previousPath, err := panelNetworkSnapshotPath(transaction)
	if err != nil {
		return err
	}
	previousConfig, err := readLimitedFile(previousPath, networkConfigSnapshotMaxBytes)
	if err != nil {
		return fmt.Errorf("读取访问配置恢复快照: %w", err)
	}
	if checksumHex(previousConfig) != transaction.PreviousSHA256 {
		return errors.New("访问配置恢复快照校验失败")
	}
	return replacePanelConfigFile(previousConfig)
}

func replacePanelConfigFile(contents []byte) error {
	configPath := configFilePath()
	directory := filepath.Dir(configPath)
	temporary, err := os.CreateTemp(directory, ".config-network-recovery-*.yaml")
	if err != nil {
		return fmt.Errorf("创建访问配置恢复文件: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, configPath); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func savePanelNetworkTransaction(transaction *panelNetworkTransaction) error {
	if transaction == nil || !validNetworkTransactionID(transaction.ID) {
		return errors.New("invalid panel network transaction")
	}
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return fmt.Errorf("编码访问配置事务: %w", err)
	}
	data = append(data, '\n')
	directory := panelNetworkTransactionDirectory()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	path := filepath.Join(directory, transaction.ID+".json")
	temporary, err := os.CreateTemp(directory, ".network-transaction-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func loadPanelNetworkTransaction(transactionID string) (*panelNetworkTransaction, error) {
	if !validNetworkTransactionID(transactionID) {
		return nil, ErrNetworkTransactionNotFound
	}
	path := filepath.Join(panelNetworkTransactionDirectory(), transactionID+".json")
	data, err := readLimitedFile(path, networkTransactionMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNetworkTransactionNotFound
	}
	if err != nil {
		return nil, err
	}
	var transaction panelNetworkTransaction
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&transaction); err != nil {
		return nil, fmt.Errorf("解析访问配置事务: %w", err)
	}
	if transaction.ID != transactionID ||
		!validNetworkTransactionStatus(transaction.Status) ||
		transaction.PreviousFileName != transaction.ID+".previous.yaml" {
		return nil, errors.New("访问配置事务内容无效")
	}
	return &transaction, nil
}

func latestPanelNetworkTransaction() (*panelNetworkTransaction, error) {
	transactions, err := listPanelNetworkTransactions()
	if err != nil {
		return nil, err
	}
	if len(transactions) == 0 {
		return nil, ErrNetworkTransactionNotFound
	}
	return transactions[0], nil
}

func latestPendingPanelNetworkTransaction() (*panelNetworkTransaction, error) {
	transactions, err := listPanelNetworkTransactions()
	if err != nil {
		return nil, err
	}
	for _, transaction := range transactions {
		if transaction.Status == networkApplyStatusScheduled ||
			transaction.Status == networkApplyStatusApplying {
			return transaction, nil
		}
	}
	return nil, ErrNetworkTransactionNotFound
}

func pendingPanelNetworkTransaction() (*panelNetworkTransaction, bool) {
	transaction, err := latestPendingPanelNetworkTransaction()
	return transaction, err == nil
}

func listPanelNetworkTransactions() ([]*panelNetworkTransaction, error) {
	entries, err := os.ReadDir(panelNetworkTransactionDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNetworkTransactionNotFound
	}
	if err != nil {
		return nil, err
	}
	transactions := make([]*panelNetworkTransaction, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		transaction, loadErr := loadPanelNetworkTransaction(strings.TrimSuffix(name, ".json"))
		if loadErr == nil {
			transactions = append(transactions, transaction)
		}
	}
	sort.Slice(transactions, func(i, j int) bool {
		if transactions[i].CreatedAt.Equal(transactions[j].CreatedAt) {
			return transactions[i].ID > transactions[j].ID
		}
		return transactions[i].CreatedAt.After(transactions[j].CreatedAt)
	})
	return transactions, nil
}

func removePanelNetworkSnapshot(transaction *panelNetworkTransaction) error {
	path, err := panelNetworkSnapshotPath(transaction)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func panelNetworkSnapshotPath(transaction *panelNetworkTransaction) (string, error) {
	if transaction == nil ||
		!validNetworkTransactionID(transaction.ID) ||
		transaction.PreviousFileName != transaction.ID+".previous.yaml" {
		return "", errors.New("访问配置恢复快照路径无效")
	}
	return filepath.Join(panelNetworkTransactionDirectory(), transaction.PreviousFileName), nil
}

func panelNetworkTransactionDirectory() string {
	return filepath.Join(filepath.Dir(configFilePath()), "data", "network-transactions")
}

func randomNetworkTransactionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validNetworkTransactionID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func validNetworkTransactionStatus(value string) bool {
	switch value {
	case networkApplyStatusScheduled,
		networkApplyStatusApplying,
		networkApplyStatusSucceeded,
		networkApplyStatusRolledBack,
		networkApplyStatusFailed:
		return true
	default:
		return false
	}
}

func checksumHex(contents []byte) string {
	sum := sha256.Sum256(contents)
	return hex.EncodeToString(sum[:])
}

func writeExclusiveFile(path string, contents []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	return file.Close()
}

func readLimitedFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func prunePanelNetworkTransactions(directory string, keep int) {
	transactions, err := listPanelNetworkTransactions()
	if err != nil {
		return
	}
	if keep < 0 {
		keep = 0
	}
	if len(transactions) <= keep {
		return
	}
	for _, transaction := range transactions[keep:] {
		if transaction.Status == networkApplyStatusScheduled ||
			transaction.Status == networkApplyStatusApplying {
			continue
		}
		_ = os.Remove(filepath.Join(directory, transaction.ID+".json"))
		_ = os.Remove(filepath.Join(directory, transaction.ID+".previous.yaml"))
	}
}

func withPanelNetworkTransactionLock(ctx context.Context, action func() error) error {
	directory := panelNetworkTransactionDirectory()
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	lockFile, err := os.OpenFile(
		filepath.Join(directory, ".lock"),
		os.O_CREATE|os.O_RDWR,
		0600,
	)
	if err != nil {
		return err
	}
	defer lockFile.Close()
	for {
		err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	defer unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	return action()
}

func truncateNetworkTransactionError(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\x00", ""))
	if len(value) <= 1024 {
		return value
	}
	return value[:1024] + "…"
}
