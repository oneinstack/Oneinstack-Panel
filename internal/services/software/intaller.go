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
	if err := os.MkdirAll(basePath, 0755); err != nil {
		logger.Write("创建目录失败: %v", err)
		return err
	}

	// 下载文件
	downloadPath := filepath.Join(basePath, versionName)
	logger.Write("开始下载文件: %s", targetVersion.DownloadURL)
	if err := downloadFile(targetVersion.DownloadURL, downloadPath); err != nil {
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

	return nil
}

// 更新系统PATH环境变量
func updateSystemPath(binPath string) error {
	pathEntry := fmt.Sprintf("\nexport PATH=$PATH:%s\n", binPath)
	profilePath := "/etc/profile.d/installer_path.sh"

	// 检查是否已存在该路径
	if content, err := os.ReadFile(profilePath); err == nil {
		if strings.Contains(string(content), binPath) {
			return nil
		}
	}

	f, err := os.OpenFile(profilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(pathEntry)
	return err
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

// 文件下载
func downloadFile(url, path string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
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
