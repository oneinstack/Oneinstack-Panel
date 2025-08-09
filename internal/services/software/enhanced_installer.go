package software

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"oneinstack/app"
	"oneinstack/internal/models"
	logService "oneinstack/internal/services/log"
	"oneinstack/router/input"
)

// EnhancedInstaller 增强的安装器，支持实时日志
type EnhancedInstaller struct {
	logManager *logService.InstallLogManager
}

// NewEnhancedInstaller 创建增强安装器
func NewEnhancedInstaller() *EnhancedInstaller {
	return &EnhancedInstaller{
		logManager: logService.GetLogManager(),
	}
}

// InstallWithRealTimeLog 执行安装并支持实时日志
func (ei *EnhancedInstaller) InstallWithRealTimeLog(params *input.InstallParams) (string, error) {
	// 1. 生成任务ID和日志文件名
	taskID := fmt.Sprintf("%s_%s_%d", params.Key, params.Version, time.Now().Unix())
	logFileName := fmt.Sprintf("install_%s.log", time.Now().Format("2006-01-02_15-04-05"))
	logFilePath := filepath.Join("/data/wwwlogs/install", logFileName)

	// 2. 确保日志目录存在
	if err := os.MkdirAll(filepath.Dir(logFilePath), 0755); err != nil {
		return "", fmt.Errorf("创建日志目录失败: %v", err)
	}

	// 3. 创建日志流
	ei.logManager.CreateLogStream(taskID, logFileName, params.Key)

	// 4. 更新数据库状态
	ei.updateSoftwareStatus(params, models.Soft_Status_Ing, logFileName, taskID)

	// 5. 异步执行安装
	go ei.executeInstallScript(params, logFilePath, taskID)

	return taskID, nil
}

// executeInstallScript 执行安装脚本
func (ei *EnhancedInstaller) executeInstallScript(params *input.InstallParams, logFilePath, taskID string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("Install script panic: %v\n", r)
		}
		// 清理日志流
		ei.logManager.RemoveLogStream(taskID)
	}()

	// 创建日志文件
	logFile, err := os.Create(logFilePath)
	if err != nil {
		fmt.Printf("Failed to create log file: %v\n", err)
		return
	}
	defer logFile.Close()

	// 获取脚本路径（这里需要根据实际的脚本管理逻辑调整）
	scriptPath, err := ei.getScriptPath(params)
	if err != nil {
		fmt.Printf("Failed to get script path: %v\n", err)
		ei.updateSoftwareStatus(params, models.Soft_Status_Default, "", taskID)
		return
	}

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

	// 创建完成标志文件
	ei.createCompletionMarker(params.Key)

	// 更新最终状态
	ei.updateSoftwareStatus(params, status, "", taskID)
	ei.updateSoftwareInstallInfo(params, installed, installVersion)
}

// getScriptPath 获取脚本路径
func (ei *EnhancedInstaller) getScriptPath(params *input.InstallParams) (string, error) {
	// 这里应该根据实际的脚本管理逻辑来获取脚本路径
	// 可能需要从embed.FS中提取脚本或者从数据库中获取脚本信息

	// 简化实现，假设脚本在固定目录
	scriptPath := fmt.Sprintf("/tmp/install_%s_%s.sh", params.Key, params.Version)

	// 这里应该有生成或复制脚本的逻辑
	// 暂时返回一个示例路径
	return scriptPath, nil
}

// createCompletionMarker 创建完成标志文件
func (ei *EnhancedInstaller) createCompletionMarker(softName string) {
	markerPath := filepath.Join("/usr/local/one/logs", softName+"-end.log")

	// 确保目录存在
	os.MkdirAll(filepath.Dir(markerPath), 0755)

	// 创建标志文件
	file, err := os.Create(markerPath)
	if err != nil {
		fmt.Printf("Failed to create completion marker: %v\n", err)
		return
	}
	defer file.Close()

	file.WriteString(fmt.Sprintf("Installation completed at: %s\n", time.Now().Format(time.RFC3339)))
}

// updateSoftwareStatus 更新软件状态
func (ei *EnhancedInstaller) updateSoftwareStatus(params *input.InstallParams, status int, logFileName, taskID string) {
	updateData := map[string]interface{}{
		"status": status,
	}

	if logFileName != "" {
		updateData["log"] = logFileName
	}

	if taskID != "" {
		updateData["task_id"] = taskID
	}

	app.DB().Model(&models.Software{}).
		Where("key = ? and version = ?", params.Key, params.Version).
		Updates(updateData)
}

// updateSoftwareInstallInfo 更新软件安装信息
func (ei *EnhancedInstaller) updateSoftwareInstallInfo(params *input.InstallParams, installed bool, version string) {
	app.DB().Model(&models.Software{}).
		Where("key = ? and version = ?", params.Key, params.Version).
		Updates(map[string]interface{}{
			"installed":       installed,
			"install_version": version,
		})
}
