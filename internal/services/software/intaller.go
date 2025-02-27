package software

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"oneinstack/internal/models"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// 安装软件
func InstallSoftwareAsync(soft *models.Softwaren, params map[string]map[string]string, rootPath string) (string, error) {
	// 创建日志记录器
	logger, err := NewInstallLogger(soft.Name, soft.Versions[0].Version)
	if err != nil {
		return "", err
	}
	defer logger.Close()

	// 创建安装任务
	job := &InstallJob{
		SoftwareName: soft.Name,
		Version:      soft.Versions[0].Version,
		LogPath:      logger.LogPath,
		Status:       "pending",
		CreatedAt:    time.Now(),
	}

	key := fmt.Sprintf("%s-%s", job.SoftwareName, job.Version)
	jobMutex.Lock()
	installJobs[key] = job
	jobMutex.Unlock()

	// 启动异步安装
	go func() {
		jobMutex.Lock()
		job.Status = "running"
		jobMutex.Unlock()

		logger.Write("开始安装 %s 版本 %s", soft.Name, soft.Versions[0].Version)
		err := installSoftware(soft, params, rootPath, logger)

		jobMutex.Lock()
		defer jobMutex.Unlock()
		if err != nil {
			logger.Write("安装失败: %v", err)
			job.Status = "failed"
		} else {
			logger.Write("安装成功")
			job.Status = "completed"
		}
	}()

	return logger.LogPath, nil
}

// 原InstallSoftware改为私有方法
func installSoftware(soft *models.Softwaren, params map[string]map[string]string, rootPath string, logger *InstallLogger) error {
	if _, err := os.Stat(rootPath); os.IsNotExist(err) {
		logger.Write("创建安装目录: %s", rootPath)
		os.MkdirAll(rootPath, 0755)
	}
	targetVersion := soft.Versions[0]
	versionName := targetVersion.VersionName
	// 创建安装目录
	basePath := renderTemplate(targetVersion.InstallConfig.BasePath, map[string]interface{}{
		"root":    rootPath,
		"name":    soft.Name,
		"version": targetVersion.Version,
	})
	logger.Write("生成基础路径: %s", basePath)
	fmt.Println(basePath)
	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Write("创建目录失败: %v", err)
		return err
	}

	// 下载文件
	downloadPath, err := downloadFile(targetVersion.DownloadURL, basePath)
	if err != nil {
		logger.Write("下载失败: %v", err)
		return err
	}
	logger.Write("下载完成，保存到: %s", downloadPath)

	// 创建bin目录并设置环境变量
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		logger.Write("创建bin目录失败: %v", err)
		return err
	}
	if err := updateSystemPath(binPath); err != nil {
		logger.Write("更新PATH失败: %v", err)
		return fmt.Errorf("failed to update PATH: %v", err)
	}
	confPath := filepath.Join(basePath, "conf")
	if err := os.MkdirAll(confPath, 0755); err != nil {
		logger.Write("创建conf目录失败: %v", err)
		return err
	}

	dataPath := filepath.Join(basePath, "data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		logger.Write("创建data目录失败: %v", err)
		return err
	}

	// 解压文件
	logger.Write("开始解压文件: %s", downloadPath)
	fmt.Println(binPath)
	if err := extractFile(downloadPath, binPath); err != nil {
		logger.Write("解压失败: %v", err)
		return err
	}
	logger.Write("解压完成到: %s", binPath)

	// 生成配置文件
	for _, templateStr := range targetVersion.InstallConfig.ConfigTemplates {
		outputPath := filepath.Join(basePath, "conf", templateStr.FileName)
		targetParams := make(map[string]string)
		for _, param := range targetVersion.InstallConfig.ConfigParams {
			targetParams[param.Name] = param.Name
		}
		logger.Write("生成配置文件: %s", outputPath)
		if err := generateConfig(templateStr.Content, templateStr.FileName, targetParams, params, outputPath); err != nil {
			logger.Write("生成配置文件失败: %v", err)
			return err
		}
	}

	// 生成系统服务配置
	serviceConfig := targetVersion.InstallConfig.ServiceConfig
	if serviceConfig.SystemdTemplate != "" {
		logger.Write("生成systemd服务配置")
		serviceContent := renderTemplate(serviceConfig.SystemdTemplate, map[string]interface{}{
			"start_cmd": renderTemplate(serviceConfig.StartCmd, map[string]interface{}{
				"conf":   confPath,
				"bin":    binPath,
				"data":   dataPath,
				"params": params,
			}),
			"stop_cmd": renderTemplate(serviceConfig.StopCmd, map[string]interface{}{
				"conf":   confPath,
				"bin":    binPath,
				"params": params,
			}),
			"bin":    binPath,
			"conf":   confPath,
			"data":   dataPath,
			"params": params,
		})

		servicePath := filepath.Join("/etc/systemd/system/", versionName+".service")
		if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
			logger.Write("写入服务文件失败: %v", err)
			return fmt.Errorf("failed to write service file: %v", err)
		}
		logger.Write("服务文件已写入: %s", servicePath)
	}

	logger.Write("重新加载systemd配置")
	if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
		logger.Write("systemd配置重载失败: %v", err)
	}

	logger.Write("启用并启动服务: %s", versionName)
	if err := exec.Command("systemctl", "enable", versionName+".service").Run(); err != nil {
		logger.Write("服务启用失败: %v", err)
	}
	if err := exec.Command("systemctl", "start", versionName+".service").Run(); err != nil {
		logger.Write("服务启动失败: %v", err)
	}

	return nil
}

// 更新系统PATH环境变量
func updateSystemPath(binPath string) error {
	binSubPath := filepath.Join(binPath, "bin")
	envFile := "/etc/environment"

	// 读取现有环境配置
	content, err := os.ReadFile(envFile)
	if err != nil {
		return fmt.Errorf("读取环境文件失败: %w", err)
	}

	// 解析现有PATH
	pathValue, exists := parseEnvVar(string(content), "PATH")
	newPaths := []string{binPath, binSubPath}

	// 构建新PATH
	var newPath string
	if exists {
		// 去重处理
		existingPaths := strings.Split(pathValue, ":")
		pathMap := make(map[string]struct{})
		for _, p := range existingPaths {
			pathMap[p] = struct{}{}
		}

		// 添加新路径（如果不存在）
		var updatedPaths []string
		for _, p := range append(existingPaths, newPaths...) {
			if _, ok := pathMap[p]; ok {
				continue
			}
			updatedPaths = append(updatedPaths, p)
			pathMap[p] = struct{}{}
		}
		newPath = strings.Join(updatedPaths, ":")
	} else {
		newPath = strings.Join(append(newPaths, os.Getenv("PATH")), ":")
	}

	// 保留其他环境变量
	var output strings.Builder
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "PATH=") {
			continue
		}
		output.WriteString(line + "\n")
	}

	// 写入新PATH（带引号兼容不同格式）
	output.WriteString(fmt.Sprintf("PATH=\"%s\"\n", newPath))

	// 写回文件
	if err := os.WriteFile(envFile, []byte(output.String()), 0644); err != nil {
		return fmt.Errorf("写入环境文件失败: %w", err)
	}

	// 立即生效（需要用户重新登录）
	return nil
}

// 解析环境变量值
func parseEnvVar(content, key string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, key+"=") {
			value := strings.TrimPrefix(line, key+"=")
			// 去除可能存在的引号
			return strings.Trim(value, `"`), true
		}
	}
	return "", false
}

// 模板渲染通用函数
func renderTemplate(tpl string, data map[string]interface{}) string {
	tmpl := template.Must(template.New("").Parse(tpl))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return ""
	}
	return buf.String()
}

// 文件下载（返回实际下载路径）
func downloadFile(urlStr string, saveDir string) (string, error) {
	// 从URL提取文件名
	fileName := filepath.Base(urlStr)
	// 去除可能的查询参数
	if cleanName := strings.Split(fileName, "?"); len(cleanName) > 0 {
		fileName = cleanName[0]
	}

	// 创建保存目录
	if err := os.MkdirAll(saveDir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败: %w", err)
	}

	// 发起HTTP请求
	resp, err := http.Get(urlStr)
	if err != nil {
		return "", fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("无效状态码: %d", resp.StatusCode)
	}

	// 创建目标文件
	filePath := filepath.Join(saveDir, fileName)
	out, err := os.Create(filePath)
	if err != nil {
		return "", fmt.Errorf("创建文件失败: %w", err)
	}
	defer out.Close()

	// 复制数据
	if _, err = io.Copy(out, resp.Body); err != nil {
		return "", fmt.Errorf("下载失败: %w", err)
	}

	return filePath, nil
}

// 文件解压
func extractFile(src, dest string) error {
	switch {
	case strings.HasSuffix(src, ".tar.gz"):
		return exec.Command("tar", "xzf", src, "-C", dest).Run()
	case strings.HasSuffix(src, ".tar.xz"):
		return exec.Command("tar", "xJf", src, "-C", dest).Run()
	default:
		return fmt.Errorf("unsupported11 file format")
	}
}

// 生成配置文件
func generateConfig(templateStr string, confFile string, targetParams map[string]string, params map[string]map[string]string, outputPath string) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
		return err
	}
	tmpl := template.Must(template.New("config").Parse(templateStr))
	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()
	exParams := make(map[string]string)
	paravs := params[confFile]
	for k, _ := range targetParams {
		exParams[k] = paravs[k]
	}
	fmt.Println(templateStr)
	fmt.Println(exParams)
	return tmpl.Execute(file, exParams)
}
