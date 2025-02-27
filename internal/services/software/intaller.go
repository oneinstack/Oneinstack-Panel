package software

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"oneinstack/internal/models"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// 配置结构体映射
type SoftwareConfig struct {
	Software []*models.Softwaren `json:"software"`
}

// 加载软件配置
func LoadSoftwareConfig(path string) (*SoftwareConfig, error) {
	var config SoftwareConfig
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(content, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// 安装软件
func InstallSoftware(softwareName, version string, params map[string]map[string]string, rootPath string) error {
	configs, err := LoadSoftwareConfig("software.json")
	if err != nil {
		return err
	}
	targetVersion := models.Version{}
	found := false
	versionName := ""
	for _, s := range configs.Software {
		if s.Name == softwareName {
			for _, v := range s.Versions {
				versionName = v.VersionName
				if v.Version == version {
					targetVersion = v
					found = true
					break
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("software %s version %s not found", softwareName, version)
	}

	// 创建安装目录
	basePath := renderTemplate(targetVersion.InstallConfig.BasePath, map[string]interface{}{
		"root":    rootPath,
		"name":    softwareName,
		"version": version,
	})
	if err := os.MkdirAll(basePath, 0755); err != nil {
		fmt.Println(err.Error())
		return err
	}

	// 下载文件
	downloadPath := filepath.Join(basePath, versionName)
	if err := downloadFile(targetVersion.DownloadURL, downloadPath); err != nil {
		fmt.Println(err.Error())
		return err
	}

	// 创建bin目录并设置环境变量
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		return err
	}
	if err := updateSystemPath(binPath); err != nil {
		return fmt.Errorf("failed to update PATH: %v", err)
	}
	confPath := filepath.Join(basePath, "conf")
	if err := os.MkdirAll(confPath, 0755); err != nil {
		return err
	}

	dataPath := filepath.Join(basePath, "data")
	if err := os.MkdirAll(dataPath, 0755); err != nil {
		return err
	}

	// 解压文件
	if err := extractFile(downloadPath, binPath); err != nil {
		fmt.Println("123" + err.Error())
		return err
	}

	// 生成配置文件
	for _, templateStr := range targetVersion.InstallConfig.ConfigTemplates {
		outputPath := filepath.Join(basePath, "conf", templateStr.FileName)
		targetParams := make(map[string]string)
		for _, param := range targetVersion.InstallConfig.ConfigParams {
			targetParams[param.Name] = param.Name
		}
		if err := generateConfig(templateStr.Content, templateStr.FileName, targetParams, params, outputPath); err != nil {
			return err
		}
	}

	// 生成系统服务配置
	serviceConfig := targetVersion.InstallConfig.ServiceConfig
	if serviceConfig.SystemdTemplate != "" {
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

		servicePath := filepath.Join("/etc/systemd/system/", softwareName+".service")
		if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
			return fmt.Errorf("failed to write service file: %v", err)
		}
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
