package script

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	"oneinstack/router/input"
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
	Name    string            // 脚本名称，如 nginx, mysql55
	Type    ScriptType        // 脚本类型
	Content string            // 脚本内容
	Params  map[string]string // 脚本参数
	Version string            // 软件版本
}

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
	// 确保目录存在
	if err := sm.ensureDirectories(); err != nil {
		return "", err
	}

	// 处理脚本参数
	processedContent := sm.processScriptParams(scriptInfo.Content, scriptInfo.Params)

	// 创建临时脚本文件
	scriptPath, err := sm.createTempScript(scriptInfo.Name, processedContent)
	if err != nil {
		return "", err
	}

	// 创建日志文件
	logFileName := fmt.Sprintf("%s_%s_%s.log",
		scriptInfo.Type,
		scriptInfo.Name,
		time.Now().Format("2006-01-02_15-04-05"))
	logPath := filepath.Join(sm.logDir, logFileName)

	if async {
		// 异步执行
		go sm.executeScriptAsync(scriptPath, logPath, params)
		return logFileName, nil
	} else {
		// 同步执行
		return sm.executeScriptSync(scriptPath, logPath, params)
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

	file, err := os.OpenFile(scriptPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0755)
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
func (sm *ScriptManager) executeScriptSync(scriptPath, logPath string, params *input.InstallParams) (string, error) {
	// 创建日志文件
	logFile, err := os.Create(logPath)
	if err != nil {
		return "", fmt.Errorf("failed to create log file: %v", err)
	}
	defer logFile.Close()

	// 执行脚本
	cmd := exec.Command("bash", scriptPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Run(); err != nil {
		return filepath.Base(logPath), fmt.Errorf("script execution failed: %v", err)
	}

	// 清理临时脚本文件
	os.Remove(scriptPath)

	return filepath.Base(logPath), nil
}

// executeScriptAsync 异步执行脚本
func (sm *ScriptManager) executeScriptAsync(scriptPath, logPath string, params *input.InstallParams) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Script execution panic: %v\n", r)
		}
		// 清理临时脚本文件
		os.Remove(scriptPath)
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

	// 执行脚本
	cmd := exec.Command("bash", scriptPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	var status int
	var installed bool
	var installVersion string

	if err := cmd.Run(); err != nil {
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
	app.DB().Model(&models.Software{}).
		Where("key = ? and version = ?", params.Key, params.Version).
		Updates(map[string]interface{}{
			"installed":       installed,
			"install_version": version,
		})
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
