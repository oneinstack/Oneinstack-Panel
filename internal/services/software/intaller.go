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
		fmt.Println("下载失败", err.Error())
		return err
	}
	logger.Write("下载完成，保存到: %s", downloadPath)

	// 创建bin目录并设置环境变量
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		fmt.Println("创建bin目录失败: %v", err)
		logger.Write("创建bin目录失败: %v", err)
		return err
	}

	confPath := filepath.Join(basePath, "conf")
	if err := os.MkdirAll(confPath, 0755); err != nil {
		fmt.Println("创建conf目录失败: %v", err)
		logger.Write("创建conf目录失败: %v", err)
		return err
	}

	dataPath := filepath.Join(basePath, "data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		fmt.Println("创建data目录失败: %v", err)
		logger.Write("创建data目录失败: %v", err)
		return err
	}

	// 解压文件
	logger.Write("开始解压文件: %s", downloadPath)
	fmt.Println(binPath)
	if err := extractFile(downloadPath, binPath); err != nil {
		logger.Write("解压失败: %v", err)
		fmt.Println("下载失败", err.Error())
		return err
	}
	fmt.Println("解压完成到: %s", binPath)
	logger.Write("解压完成到: %s", binPath)

	if err := updateSystemPath(binPath); err != nil {
		fmt.Println("更新PATH失败: %v", err)
		logger.Write("更新PATH失败: %v", err)
		return fmt.Errorf("failed to update PATH: %v", err)
	}

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
	envFile := "./profile"
	if _, err := os.Stat(envFile); os.IsNotExist(err) {
		os.Create(envFile)
	}

	// 读取现有配置
	content, err := os.ReadFile(envFile)
	if err != nil {
		return fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 检查路径是否已存在
	existingContent := string(content)
	pathsToAdd := []string{binPath, binSubPath}
	var needUpdate bool

	// 构建新的PATH设置
	var newLines []string
	for _, path := range pathsToAdd {
		// 检查是否已存在该路径
		if !strings.Contains(existingContent, fmt.Sprintf(":$PATH:%s", path)) {
			newLines = append(newLines, fmt.Sprintf("export PATH=$PATH:%s", path))
			needUpdate = true
		}
	}

	if !needUpdate {
		return nil
	}

	// 追加新配置到文件末尾
	file, err := os.OpenFile(envFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开配置文件失败: %w", err)
	}
	defer file.Close()

	// 写入新的PATH设置
	if _, err := file.WriteString("\n\n# Added by onesoft installer\n"); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	for _, line := range newLines {
		if _, err := file.WriteString(line + "\n"); err != nil {
			return fmt.Errorf("写入配置失败: %w", err)
		}
	}

	// 立即生效（需要root权限）
	cmd := exec.Command("/bin/bash", "-c", "source /etc/profile && export -p")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("刷新环境变量失败: %w\n输出: %s", err, string(output))
	}

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
	fmt.Println("下载文件", urlStr)
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
	// 创建临时目录用于检查文件结构
	tmpDir, err := os.MkdirTemp("", "extract-")
	if err != nil {
		return fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// 先解压到临时目录检查结构
	cmd := exec.Command("tar", "xf", src, "-C", tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("临时解压失败: %w\n输出: %s", err, output)
	}

	// 检查解压后的目录结构
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败: %w", err)
	}

	// 判断是否需要剥离顶层目录
	stripComponents := 0
	if len(entries) == 1 && entries[0].IsDir() {
		// 如果只有一个目录，需要剥离顶层
		stripComponents = 1
	}

	// 根据文件类型构建解压命令
	args := []string{"-x"}
	switch {
	case strings.HasSuffix(src, ".tar.gz"):
		args = append(args, "-z")
	case strings.HasSuffix(src, ".tar.xz"):
		args = append(args, "-J")
	default:
		return fmt.Errorf("unsupported file format")
	}

	// 添加剥离目录参数
	if stripComponents > 0 {
		args = append(args, fmt.Sprintf("--strip-components=%d", stripComponents))
	}

	// 添加必要参数（顺序很重要！）
	args = append(args,
		"-f", src,
		"-C", dest,
	)

	// 执行正式解压
	cmd = exec.Command("tar", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("解压失败: %w\n命令: %s\n输出: %s", err, cmd.String(), output)
	}

	return nil
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
