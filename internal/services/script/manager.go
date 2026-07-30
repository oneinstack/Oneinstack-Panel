package script

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"

	"gorm.io/gorm"
)

// ScriptManager 脚本管理器
type ScriptManager struct {
	tempDir string
	logDir  string
}

// NewScriptManager 创建新的脚本管理器
func NewScriptManager() *ScriptManager {
	return &ScriptManager{
		tempDir: "/tmp/oneinstack-scripts",
		logDir:  "/data/wwwlogs/install",
	}
}

// ScriptType 脚本类型
type ScriptType string

const (
	ScriptTypeInstall   ScriptType = "install"
	ScriptTypeUninstall ScriptType = "uninstall"
	ScriptTypeConfig    ScriptType = "config"
)

// ScriptInfo 脚本信息
type ScriptInfo struct {
	Name           string            // 脚本名称，如 nginx, mysql55
	Type           ScriptType        // 脚本类型
	Content        string            // 旧版内置脚本内容
	Path           string            // 已验证组件包中的主动作路径
	PrecheckPath   string            // 安装前检查
	ConfigurePath  string            // 安装后配置
	VerifyPath     string            // 安装结果验证
	RollbackPath   string            // 失败回滚
	WorkingDir     string            // 组件包根目录
	Source         string            // remote/cache/bundled/legacy
	Params         map[string]string // 仅通过环境变量传给组件动作
	ParameterSpecs []ParameterSpec   // 清单声明的参数约束
	ActionName     string            // install/upgrade/uninstall
	Timeouts       map[string]time.Duration
	Version        string // 软件版本
	PackageVersion string // 组件脚本包版本
}

type ParameterSpec struct {
	Name     string
	Type     string
	Required bool
	Secret   bool
	Default  string
}

type ExecutionObserver interface {
	OnActionStart(action string)
	OnActionProgress(action string, percent int, code, message string)
	OnActionComplete(action string)
	OnRollbackStart()
	OnRollbackComplete(err error)
}

var componentExecutionLocks sync.Map
var progressCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// GetScript 获取脚本内容
func (sm *ScriptManager) GetScript(scriptType ScriptType, name string) (*ScriptInfo, error) {
	scriptPath := fmt.Sprintf("scripts/%s/%s.sh", scriptType, name)

	content, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("script not found: %s", scriptPath)
	}

	return &ScriptInfo{
		Name:    name,
		Type:    scriptType,
		Content: string(content),
		Params:  make(map[string]string),
	}, nil
}

// ListScripts 列出所有脚本
func (sm *ScriptManager) ListScripts(scriptType ScriptType) ([]string, error) {
	dirPath := fmt.Sprintf("scripts/%s", scriptType)

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}

	var scripts []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sh") {
			name := strings.TrimSuffix(entry.Name(), ".sh")
			scripts = append(scripts, name)
		}
	}

	return scripts, nil
}

// ExecuteScript 执行脚本
func (sm *ScriptManager) ExecuteScript(scriptInfo *ScriptInfo, params *input.InstallParams, async bool) (string, error) {
	if err := validateParameters(scriptInfo); err != nil {
		return "", err
	}
	release, acquired := acquireComponentExecution(scriptInfo.Name)
	if !acquired {
		return "", fmt.Errorf("component %s already has an operation in progress", scriptInfo.Name)
	}
	releaseNeeded := true
	defer func() {
		if releaseNeeded {
			release()
		}
	}()

	// 确保目录存在
	if err := sm.ensureDirectories(); err != nil {
		return "", err
	}

	scriptPath := scriptInfo.Path
	temporaryScript := false
	if scriptPath == "" {
		// 兼容旧版内置脚本；新组件包不再做字符串模板替换。
		processedContent := sm.processScriptParams(scriptInfo.Content, scriptInfo.Params)
		createdPath, err := sm.createTempScript(scriptInfo.Name, processedContent)
		if err != nil {
			return "", err
		}
		scriptPath = createdPath
		temporaryScript = true
	}

	// 创建日志文件
	logFileName := fmt.Sprintf("%s_%s_%s.log",
		scriptInfo.Type,
		scriptInfo.Name,
		time.Now().Format("2006-01-02_15-04-05"))
	logPath := filepath.Join(sm.logDir, logFileName)

	if async {
		// 异步执行
		releaseNeeded = false
		go sm.executeScriptAsync(scriptInfo, scriptPath, temporaryScript, logPath, params, release)
		return logFileName, nil
	} else {
		// 同步执行
		return sm.executeScriptSync(scriptInfo, scriptPath, temporaryScript, logPath, params)
	}
}

// ensureDirectories 确保必要目录存在
func (sm *ScriptManager) ensureDirectories() error {
	dirs := []string{sm.tempDir, sm.logDir}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
	}
	return nil
}

// processScriptParams 处理脚本参数替换
func (sm *ScriptManager) processScriptParams(content string, params map[string]string) string {
	processedContent := content
	for key, value := range params {
		placeholder := fmt.Sprintf("{{%s}}", key)
		processedContent = strings.ReplaceAll(processedContent, placeholder, value)
	}
	return processedContent
}

// createTempScript 创建临时脚本文件
func (sm *ScriptManager) createTempScript(name, content string) (string, error) {
	scriptPath := filepath.Join(sm.tempDir, fmt.Sprintf("%s_%d.sh", name, time.Now().UnixNano()))

	file, err := os.OpenFile(scriptPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0700)
	if err != nil {
		return "", fmt.Errorf("failed to create script file: %v", err)
	}
	defer file.Close()

	if _, err := file.WriteString(content); err != nil {
		return "", fmt.Errorf("failed to write script content: %v", err)
	}

	return scriptPath, nil
}

// executeScriptSync 同步执行脚本
func (sm *ScriptManager) executeScriptSync(scriptInfo *ScriptInfo, scriptPath string, temporaryScript bool, logPath string, params *input.InstallParams) (string, error) {
	// 创建日志文件
	logFile, err := os.Create(logPath)
	if err != nil {
		return "", fmt.Errorf("failed to create log file: %v", err)
	}
	defer logFile.Close()

	if temporaryScript {
		defer os.Remove(scriptPath)
	}
	if err := sm.runInstallActions(scriptInfo, scriptPath, logFile); err != nil {
		return filepath.Base(logPath), err
	}

	return filepath.Base(logPath), nil
}

// ExecuteScriptTask runs a component synchronously under a caller-owned
// context and publishes structured action progress. It is the production task
// path; ExecuteScript remains available for one release as a compatibility
// wrapper for older callers.
func (sm *ScriptManager) ExecuteScriptTask(
	ctx context.Context,
	scriptInfo *ScriptInfo,
	params *input.InstallParams,
	logPath string,
	observer ExecutionObserver,
) (string, error) {
	if err := validateParameters(scriptInfo); err != nil {
		return "", err
	}
	release, acquired := acquireComponentExecution(scriptInfo.Name)
	if !acquired {
		return "", fmt.Errorf("component %s already has an operation in progress", scriptInfo.Name)
	}
	defer release()

	if err := sm.ensureDirectories(); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0750); err != nil {
		return "", fmt.Errorf("create task log directory: %w", err)
	}

	scriptPath := scriptInfo.Path
	temporaryScript := false
	if scriptPath == "" {
		processedContent := sm.processScriptParams(scriptInfo.Content, scriptInfo.Params)
		createdPath, err := sm.createTempScript(scriptInfo.Name, processedContent)
		if err != nil {
			return "", err
		}
		scriptPath = createdPath
		temporaryScript = true
	}
	if temporaryScript {
		defer os.Remove(scriptPath)
	}

	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("create task log file: %w", err)
	}
	redactedLog := newRedactingWriter(logFile, secretParameterValues(scriptInfo))
	defer func() {
		_ = redactedLog.Flush()
		_ = logFile.Close()
	}()

	logName := filepath.Base(logPath)
	updatesInstallState := !isServiceControlAction(scriptInfo.ActionName)
	if updatesInstallState {
		sm.updateSoftwareStatus(params, models.Soft_Status_Ing, logName)
	}
	if err := sm.runInstallActionsContext(ctx, scriptInfo, scriptPath, redactedLog, observer); err != nil {
		if updatesInstallState {
			sm.updateSoftwareStatus(params, models.Soft_Status_Err, logName)
		}
		return logName, err
	}
	if !updatesInstallState {
		return logName, nil
	}
	if strings.EqualFold(scriptInfo.ActionName, "uninstall") ||
		scriptInfo.Type == ScriptTypeUninstall {
		sm.updateSoftwareStatus(params, models.Soft_Status_Default, logName)
		sm.updateSoftwareInstallInfo(params, false, "")
		return logName, nil
	}
	sm.updateSoftwareStatus(params, models.Soft_Status_Suc, logName)
	sm.updateSoftwareInstallInfo(params, true, params.Version)
	return logName, nil
}

// ExecuteProbe runs a signed, fixed read-only action without changing software
// installation state. Probe output is bounded before it reaches the API layer.
func (sm *ScriptManager) ExecuteProbe(
	ctx context.Context,
	scriptInfo *ScriptInfo,
	maxBytes int,
) ([]byte, error) {
	if scriptInfo == nil {
		return nil, errors.New("component probe action is missing")
	}
	actionName := strings.TrimSpace(scriptInfo.ActionName)
	if !strings.EqualFold(actionName, "status") &&
		!strings.EqualFold(actionName, "configGet") {
		return nil, errors.New("only component status and configuration read actions may run as probes")
	}
	if err := validateParameters(scriptInfo); err != nil {
		return nil, err
	}
	if strings.TrimSpace(scriptInfo.Path) == "" {
		return nil, errors.New("component probe action path is missing")
	}
	if maxBytes < 1 || maxBytes > 1024*1024 {
		return nil, errors.New("probe output limit must be between 1 byte and 1 MiB")
	}
	var output bytes.Buffer
	bounded := &boundedWriter{target: &output, remaining: maxBytes}
	if err := sm.runActionContext(
		ctx,
		actionName,
		scriptInfo.Path,
		scriptInfo.WorkingDir,
		scriptInfo.Params,
		scriptInfo.timeout(actionName),
		bounded,
		nil,
	); err != nil {
		return nil, fmt.Errorf("%s action failed: %w", actionName, err)
	}
	return output.Bytes(), nil
}

type boundedWriter struct {
	target    io.Writer
	remaining int
}

func (w *boundedWriter) Write(data []byte) (int, error) {
	if len(data) > w.remaining {
		return 0, errors.New("component probe output exceeded limit")
	}
	w.remaining -= len(data)
	return w.target.Write(data)
}

// executeScriptAsync 异步执行脚本
func (sm *ScriptManager) executeScriptAsync(scriptInfo *ScriptInfo, scriptPath string, temporaryScript bool, logPath string, params *input.InstallParams, release func()) {
	defer func() {
		release()
		if r := recover(); r != nil {
			fmt.Printf("Script execution panic: %v\n", r)
		}
		if temporaryScript {
			os.Remove(scriptPath)
		}
	}()

	// 创建日志文件
	logFile, err := os.Create(logPath)
	if err != nil {
		fmt.Printf("Failed to create log file: %v\n", err)
		return
	}
	defer logFile.Close()

	// 更新软件状态为安装中
	sm.updateSoftwareStatus(params, models.Soft_Status_Ing, filepath.Base(logPath))

	var status int
	var installed bool
	var installVersion string

	if err := sm.runInstallActions(scriptInfo, scriptPath, logFile); err != nil {
		fmt.Printf("Script execution failed: %v\n", err)
		status = models.Soft_Status_Default
		installed = false
		installVersion = ""
	} else {
		fmt.Println("Script execution successful")
		status = models.Soft_Status_Suc
		installed = true
		installVersion = params.Version
	}

	// 更新最终状态
	sm.updateSoftwareStatus(params, status, filepath.Base(logPath))
	sm.updateSoftwareInstallInfo(params, installed, installVersion)
}

func (sm *ScriptManager) runInstallActions(scriptInfo *ScriptInfo, installPath string, output *os.File) error {
	return sm.runInstallActionsContext(context.Background(), scriptInfo, installPath, output, nil)
}

func (sm *ScriptManager) runInstallActionsContext(
	ctx context.Context,
	scriptInfo *ScriptInfo,
	installPath string,
	output io.Writer,
	observer ExecutionObserver,
) error {
	mainAction := scriptInfo.ActionName
	if mainAction == "" {
		mainAction = string(scriptInfo.Type)
	}
	actions := []struct {
		name string
		path string
	}{
		{name: "precheck", path: scriptInfo.PrecheckPath},
		{name: mainAction, path: installPath},
	}
	if mainAction != "uninstall" && !isServiceControlAction(mainAction) {
		actions = append(actions,
			struct {
				name string
				path string
			}{name: "configure", path: scriptInfo.ConfigurePath},
			struct {
				name string
				path string
			}{name: "verify", path: scriptInfo.VerifyPath},
		)
	}
	for _, action := range actions {
		if action.path == "" {
			continue
		}
		if observer != nil {
			observer.OnActionStart(action.name)
		}
		if _, err := fmt.Fprintf(output, "\n===== %s =====\n", action.name); err != nil {
			return fmt.Errorf("write action log: %w", err)
		}
		if err := sm.runActionContext(
			ctx,
			action.name,
			action.path,
			scriptInfo.WorkingDir,
			scriptInfo.Params,
			scriptInfo.timeout(action.name),
			output,
			observer,
		); err != nil {
			if scriptInfo.RollbackPath != "" && action.name != "precheck" &&
				!isServiceControlAction(mainAction) {
				_, _ = fmt.Fprintln(output, "\n===== rollback =====")
				if observer != nil {
					observer.OnRollbackStart()
				}
				rollbackErr := sm.runActionContext(
					context.Background(),
					"rollback",
					scriptInfo.RollbackPath,
					scriptInfo.WorkingDir,
					scriptInfo.Params,
					scriptInfo.timeout("rollback"),
					output,
					nil,
				)
				if observer != nil {
					observer.OnRollbackComplete(rollbackErr)
				}
				if rollbackErr != nil {
					return fmt.Errorf("%s action failed: %v; rollback failed: %v", action.name, err, rollbackErr)
				}
			}
			return fmt.Errorf("%s action failed: %w", action.name, err)
		}
		if observer != nil {
			observer.OnActionComplete(action.name)
		}
	}
	return nil
}

func isServiceControlAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "status", "start", "stop", "restart", "reload", "configget", "configapply":
		return true
	default:
		return false
	}
}

func (sm *ScriptManager) runAction(actionPath, workingDir string, params map[string]string, timeout time.Duration, output *os.File) error {
	return sm.runActionContext(
		context.Background(),
		"",
		actionPath,
		workingDir,
		params,
		timeout,
		output,
		nil,
	)
}

func (sm *ScriptManager) runActionContext(
	parent context.Context,
	actionName string,
	actionPath string,
	workingDir string,
	params map[string]string,
	timeout time.Duration,
	output io.Writer,
	observer ExecutionObserver,
) error {
	info, err := os.Lstat(actionPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("action path is not a regular file")
	}
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", actionPath)
	cmd.Dir = workingDir
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		processGroupID := cmd.Process.Pid
		err := syscall.Kill(-processGroupID, syscall.SIGTERM)
		go func() {
			timer := time.NewTimer(10 * time.Second)
			defer timer.Stop()
			<-timer.C
			if ctx.Err() != nil {
				_ = syscall.Kill(-processGroupID, syscall.SIGKILL)
			}
		}()
		return err
	}
	cmd.WaitDelay = 10 * time.Second
	for key, value := range params {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	progressReader, progressWriter, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("create script progress pipe: %w", err)
	}
	defer progressReader.Close()
	cmd.ExtraFiles = []*os.File{progressWriter}
	cmd.Env = append(cmd.Env, "ONEINSTACK_PROGRESS_FD=3")

	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		consumeProgressEvents(progressReader, actionName, observer)
	}()

	if err := cmd.Start(); err != nil {
		_ = progressWriter.Close()
		<-progressDone
		return err
	}
	_ = progressWriter.Close()
	runErr := cmd.Wait()
	_ = progressReader.Close()
	<-progressDone
	if runErr != nil {
		if parent.Err() != nil {
			return parent.Err()
		}
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("action exceeded timeout %s", timeout)
		}
		return runErr
	}
	return nil
}

type structuredProgress struct {
	Type    string `json:"type"`
	Percent int    `json:"percent"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func consumeProgressEvents(reader io.Reader, actionName string, observer ExecutionObserver) {
	if observer == nil {
		_, _ = io.Copy(io.Discard, reader)
		return
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 64*1024)
	for scanner.Scan() {
		var event structuredProgress
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		if event.Type != "progress" || event.Percent < 0 || event.Percent > 100 ||
			!progressCodePattern.MatchString(event.Code) {
			continue
		}
		event.Message = sanitizeProgressMessage(event.Message)
		if event.Message == "" {
			continue
		}
		observer.OnActionProgress(actionName, event.Percent, event.Code, event.Message)
	}
}

func sanitizeProgressMessage(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(message))
	if len(message) > 256 {
		message = message[:256]
	}
	return message
}

func secretParameterValues(scriptInfo *ScriptInfo) []string {
	secretNames := make(map[string]struct{})
	for _, spec := range scriptInfo.ParameterSpecs {
		if spec.Secret || spec.Type == "password" {
			secretNames[spec.Name] = struct{}{}
		}
	}
	var values []string
	for key, value := range scriptInfo.Params {
		upperKey := strings.ToUpper(key)
		_, declaredSecret := secretNames[key]
		if !declaredSecret &&
			!strings.Contains(upperKey, "PASSWORD") &&
			!strings.Contains(upperKey, "SECRET") &&
			!strings.Contains(upperKey, "TOKEN") &&
			!strings.HasSuffix(upperKey, "_PWD") {
			continue
		}
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

type redactingWriter struct {
	mu        sync.Mutex
	target    io.Writer
	secrets   []string
	maxSecret int
	pending   string
}

func newRedactingWriter(target io.Writer, secrets []string) *redactingWriter {
	writer := &redactingWriter{target: target}
	seen := make(map[string]struct{})
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		seen[secret] = struct{}{}
		writer.secrets = append(writer.secrets, secret)
		if len(secret) > writer.maxSecret {
			writer.maxSecret = len(secret)
		}
	}
	return writer
}

func (w *redactingWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	originalLength := len(data)
	w.pending += string(data)
	if len(w.secrets) == 0 {
		_, err := io.WriteString(w.target, w.pending)
		w.pending = ""
		return originalLength, err
	}
	retain := w.maxSecret - 1
	if retain < 0 {
		retain = 0
	}
	flushAt := len(w.pending) - retain
	if flushAt <= 0 {
		return originalLength, nil
	}
	for _, secret := range w.secrets {
		searchFrom := flushAt - len(secret) + 1
		if searchFrom < 0 {
			searchFrom = 0
		}
		for index := searchFrom; index < flushAt; index++ {
			available := min(len(secret), len(w.pending)-index)
			if index+len(secret) > flushAt &&
				w.pending[index:index+available] == secret[:available] {
				flushAt = index
				break
			}
		}
	}
	if flushAt <= 0 {
		return originalLength, nil
	}
	chunk := redactExactValues(w.pending[:flushAt], w.secrets)
	if _, err := io.WriteString(w.target, chunk); err != nil {
		return 0, err
	}
	w.pending = w.pending[flushAt:]
	return originalLength, nil
}

func (w *redactingWriter) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pending == "" {
		return nil
	}
	_, err := io.WriteString(w.target, redactExactValues(w.pending, w.secrets))
	w.pending = ""
	return err
}

func redactExactValues(value string, secrets []string) string {
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func (scriptInfo *ScriptInfo) timeout(action string) time.Duration {
	if timeout := scriptInfo.Timeouts[action]; timeout > 0 {
		return timeout
	}
	return 30 * time.Minute
}

func acquireComponentExecution(component string) (func(), bool) {
	value, _ := componentExecutionLocks.LoadOrStore(component, make(chan struct{}, 1))
	lock := value.(chan struct{})
	select {
	case lock <- struct{}{}:
		return func() { <-lock }, true
	default:
		return func() {}, false
	}
}

func validateParameters(scriptInfo *ScriptInfo) error {
	if scriptInfo.Params == nil {
		scriptInfo.Params = make(map[string]string)
	}
	for _, spec := range scriptInfo.ParameterSpecs {
		switch spec.Name {
		case "PATH", "LD_PRELOAD", "LD_LIBRARY_PATH", "BASH_ENV", "ENV", "SHELLOPTS", "IFS", "CDPATH":
			return fmt.Errorf("component parameter %s is reserved", spec.Name)
		}
		value := scriptInfo.Params[spec.Name]
		if value == "" && spec.Default != "" {
			value = spec.Default
			scriptInfo.Params[spec.Name] = value
		}
		if spec.Required && value == "" {
			return fmt.Errorf("component parameter %s is required", spec.Name)
		}
		if value == "" {
			continue
		}
		if strings.ContainsRune(value, 0) || len(value) > 4096 {
			return fmt.Errorf("component parameter %s contains invalid data", spec.Name)
		}
		switch spec.Type {
		case "integer":
			if _, err := strconv.ParseInt(value, 10, 64); err != nil {
				return fmt.Errorf("component parameter %s must be an integer", spec.Name)
			}
		case "port":
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("component parameter %s must be a valid port", spec.Name)
			}
		case "boolean":
			if value != "true" && value != "false" {
				return fmt.Errorf("component parameter %s must be true or false", spec.Name)
			}
		case "path":
			cleaned := filepath.Clean(value)
			if !filepath.IsAbs(value) || cleaned != value || cleaned == string(os.PathSeparator) {
				return fmt.Errorf("component parameter %s must be a normalized absolute path", spec.Name)
			}
		case "string", "password":
		default:
			return fmt.Errorf("component parameter %s has unsupported type %s", spec.Name, spec.Type)
		}
	}
	return nil
}

// updateSoftwareStatus 更新软件状态
func (sm *ScriptManager) updateSoftwareStatus(params *input.InstallParams, status int, logFileName string) {
	app.DB().Model(&models.Software{}).
		Where("key = ? and version = ?", params.Key, params.Version).
		Updates(map[string]interface{}{
			"status": status,
			"log":    logFileName,
		})
}

// updateSoftwareInstallInfo 更新软件安装信息
func (sm *ScriptManager) updateSoftwareInstallInfo(params *input.InstallParams, installed bool, version string) {
	if installed {
		if err := app.DB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&models.Software{}).
				Where("`key` = ? AND version <> ?", params.Key, params.Version).
				Updates(map[string]interface{}{
					"installed":       false,
					"install_version": "",
					"is_update":       false,
				}).Error; err != nil {
				return err
			}
			return tx.Model(&models.Software{}).
				Where("`key` = ? AND version = ?", params.Key, params.Version).
				Updates(map[string]interface{}{
					"installed":       true,
					"install_version": version,
					"is_update":       false,
				}).Error
		}); err != nil {
			fmt.Printf("Update software install state failed: %v\n", err)
		}
		return
	}
	if err := app.DB().Model(&models.Software{}).
		Where("`key` = ? AND version = ?", params.Key, params.Version).
		Updates(map[string]interface{}{
			"installed":       false,
			"install_version": "",
		}).Error; err != nil {
		fmt.Printf("Update software install state failed: %v\n", err)
	}
}

// CleanupTempFiles 清理临时文件
func (sm *ScriptManager) CleanupTempFiles(olderThan time.Duration) error {
	entries, err := os.ReadDir(sm.tempDir)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-olderThan)

	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			filePath := filepath.Join(sm.tempDir, entry.Name())
			os.Remove(filePath)
		}
	}

	return nil
}

// GetScriptTemplate 获取脚本模板
func (sm *ScriptManager) GetScriptTemplate(scriptType ScriptType) (string, error) {
	templatePath := fmt.Sprintf("scripts/templates/%s.template", scriptType)

	content, err := os.ReadFile(templatePath)
	if err != nil {
		return "", fmt.Errorf("template not found: %s", templatePath)
	}

	return string(content), nil
}
