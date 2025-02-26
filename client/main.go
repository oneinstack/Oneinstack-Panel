package client

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// 新增参数处理结构体
type InstallParams struct {
	SoftwareName string
	Version      string
	ConfigValues map[string]interface{}
}

const (
	SystemdDir    = "/etc/systemd/system"
	ServicePrefix = "onesfot"
)

// SoftwareClient 客户端主结构体
type SoftwareClient struct {
	serverURL   string
	installRoot string
	configRoot  string
	dataRoot    string
	logRoot     string
	httpClient  *http.Client
}

// NewSoftwareClient 创建新的客户端实例
func NewSoftwareClient(serverURL, installRoot, configRoot string) *SoftwareClient {
	return &SoftwareClient{
		serverURL:   serverURL,
		installRoot: installRoot,
		configRoot:  configRoot,
		dataRoot:    filepath.Join(installRoot, "data"),
		logRoot:     filepath.Join(installRoot, "logs"),
		httpClient:  &http.Client{Timeout: 30 * time.Second},
	}
}

// 增强的参数收集方法
func (c *SoftwareClient) collectInstallParams(software *Software, version string) (*InstallParams, error) {
	params := &InstallParams{
		SoftwareName: software.Name,
		Version:      version,
		ConfigValues: make(map[string]interface{}),
	}

	// 获取版本特定参数要求
	versionConfig := getVersionConfig(software, version)

	fmt.Printf("\n配置 %s (%s)\n", software.Name, version)
	fmt.Println("请输入以下参数（按Enter使用默认值）：")

	// 合并全局和版本特定参数
	allParams := append(software.InstallConfig.ConfigParams, versionConfig.Params...)

	for _, param := range allParams {
		for {
			// 增强提示信息
			prompt := fmt.Sprintf("▸ %s [%s]", param.Description, param.Type)
			if param.Default != nil {
				prompt += fmt.Sprintf(" (默认: %v)", param.Default)
			}
			if param.Required {
				prompt += " *必填"
			}
			fmt.Printf("\033[36m%s\033[0m: ", prompt)

			input, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			input = strings.TrimSpace(input)

			// 处理布尔类型输入
			if param.Type == "boolean" {
				if input == "" {
					input = fmt.Sprintf("%v", param.Default)
				}
				value, err := parseBool(input)
				if err != nil {
					fmt.Println("\033[31m请输入 yes/no 或 true/false\033[0m")
					continue
				}
				params.ConfigValues[param.Name] = value
				break
			}

			// 处理空输入
			if input == "" {
				if param.Required && param.Default == nil {
					fmt.Println("\033[31m此参数为必填项\033[0m")
					continue
				}
				input = fmt.Sprintf("%v", param.Default)
			}

			// 类型验证
			value, err := validateInput(param.Type, input)
			if err != nil {
				fmt.Printf("\033[31m验证失败: %v\033[0m\n", err)
				continue
			}

			params.ConfigValues[param.Name] = value
			break
		}
	}
	return params, nil
}

// 新增输入验证函数
func validateInput(paramType, input string) (interface{}, error) {
	switch paramType {
	case "number":
		return strconv.Atoi(input)
	case "boolean":
		return parseBool(input)
	case "path":
		return filepath.Abs(input)
	case "password":
		return input, nil // 实际应加密处理
	default: // string
		return input, nil
	}
}

func parseBool(input string) (bool, error) {
	lower := strings.ToLower(input)
	switch lower {
	case "true", "yes", "1":
		return true, nil
	case "false", "no", "0":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean value")
	}
}

func (c *SoftwareClient) generateConfig(software *Software, version string, params map[string]interface{}) error {
	// 获取版本特定配置模板
	versionConfig := getVersionConfig(software, version)

	vars := map[string]interface{}{
		"params": params,
		"paths":  c.resolvePaths(software, version),
	}

	for relPath, templateContent := range versionConfig.ConfigTemplates {
		absPath := filepath.Join(vars["paths"].(map[string]string)["conf"], relPath)

		// 创建目录
		if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
			return err
		}

		// 生成文件内容
		content, err := resolveTemplate(templateContent, vars)
		if err != nil {
			return fmt.Errorf("模板解析失败 %s: %v", relPath, err)
		}

		// 写入文件
		if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("写入配置文件失败 %s: %v", absPath, err)
		}
	}
	return nil
}

func getVersionConfig(software *Software, version string) *Version {
	for _, v := range software.Versions {
		if v.Version == version {
			return &v
		}
	}
	return nil
}

func (c *SoftwareClient) setupService(software *Software, paths map[string]string, params map[string]interface{}) error {
	// 生成环境变量文件
	envPath := filepath.Join(paths["conf"], "environment")
	var envContent strings.Builder
	for k, v := range software.InstallConfig.EnvVars {
		envContent.WriteString(fmt.Sprintf("%s=%s\n", k, v))
	}
	if err := os.WriteFile(envPath, []byte(envContent.String()), 0644); err != nil {
		return fmt.Errorf("环境文件生成失败: %v", err)
	}

	// 生成服务名称 (onesfot-mysql@8.0.33.service)
	serviceName := fmt.Sprintf("%s-%s@%s.service", ServicePrefix, software.Name, params["Version"])

	// 合并模板变量
	vars := map[string]interface{}{
		"bin":    paths["bin"],
		"conf":   paths["conf"],
		"data":   paths["data"],
		"log":    paths["log"],
		"params": params,
	}

	// 解析命令模板
	resolveCmd := func(tpl string) (string, error) {
		return resolveTemplate(tpl, vars)
	}

	startCmd, _ := resolveCmd(software.InstallConfig.ServiceConfig.StartCmd)
	stopCmd, _ := resolveCmd(software.InstallConfig.ServiceConfig.StopCmd)

	// 生成最终systemd模板变量
	systemdVars := map[string]interface{}{
		"start_cmd": startCmd,
		"stop_cmd":  stopCmd,
		"user":      software.Name,
		"env_file":  envPath,
	}

	// 生成systemd文件
	systemdPath := filepath.Join(SystemdDir, serviceName)
	if err := generateConfigFile(
		software.InstallConfig.ServiceConfig.SystemdTemplate,
		systemdPath,
		systemdVars,
	); err != nil {
		return err
	}

	// 系统命令执行封装
	runSystemCmd := func(args ...string) error {
		cmd := exec.Command("systemctl", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s\n%s", err, output)
		}
		return nil
	}

	// 重新加载并启用服务
	if err := runSystemCmd("daemon-reload"); err != nil {
		return fmt.Errorf("systemd重载失败: %v", err)
	}
	if err := runSystemCmd("enable", serviceName); err != nil {
		return fmt.Errorf("服务启用失败: %v", err)
	}

	return nil
}

// 通用模板解析函数
func resolveTemplate(tpl string, data interface{}) (string, error) {
	tmpl, err := template.New("").Option("missingkey=error").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// 通用配置文件生成
func generateConfigFile(templateContent, filePath string, data interface{}) error {
	content, err := resolveTemplate(templateContent, data)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return err
	}

	return os.WriteFile(filePath, []byte(content), 0644)
}

// 在安装流程中添加服务设置
func (c *SoftwareClient) Install(software *Software, version string) error {
	// 创建安装目录
	if err := c.createDirs(software, version); err != nil {
		return err
	}

	// 收集参数
	params, err := c.collectInstallParams(software, version)
	if err != nil {
		return err
	}

	// 生成配置文件
	if err := c.generateConfig(software, version, params.ConfigValues); err != nil {
		return err
	}

	// 设置系统服务
	if err := c.setupService(
		software,
		c.resolvePaths(software, version),
		params.ConfigValues, // 传递map类型参数
	); err != nil {
		return fmt.Errorf("服务配置失败: %v", err)
	}

	fmt.Println("\n\033[32m安装成功！\033[0m")
	return nil
}

func (c *SoftwareClient) ServiceCommand(name, version, action string) error {
	serviceName := fmt.Sprintf("%s-%s@%s.service", ServicePrefix, name, version)
	cmd := exec.Command("systemctl", action, serviceName)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("服务操作失败: %s\n%s", err, string(output))
	}
	return nil
}

// 使用示例
// client.ServiceCommand("mysql", "8.0.33", "start")
// client.ServiceCommand("nginx", "1.23.4", "restart")
// client.ServiceCommand("nginx", "1.23.4", "restart")

func (c *SoftwareClient) resolvePaths(software *Software, version string) map[string]string {
	base := filepath.Join(c.installRoot, software.Name, "v"+version)
	return map[string]string{
		"bin":  filepath.Join(base, "bin"),
		"conf": filepath.Join(base, "conf"),
		"data": filepath.Join(c.dataRoot, software.Name, version),
		"log":  filepath.Join(c.logRoot, software.Name, version),
		"base": base,
	}
}

func (c *SoftwareClient) ListSoftware() ([]Software, error) {
	resp, err := c.httpClient.Get(c.serverURL + "/software")
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回错误: %s", resp.Status)
	}

	var list []Software
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("响应解析失败: %v", err)
	}
	return list, nil
}

// Software 表示一个软件产品的完整定义
type Software struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	InstallConfig InstallConfig `json:"install_config"`
	Versions      []Version     `json:"versions"`
}

// InstallConfig 包含软件安装配置详情
type InstallConfig struct {
	BasePath        string            `json:"base_path"`
	ConfigParams    []ConfigParam     `json:"config_params"`
	ServiceConfig   ServiceConfig     `json:"service_config"`
	EnvVars         map[string]string `json:"env_vars"`
	ConfigTemplates map[string]string `json:"config_templates"`
}

// ConfigParam 表示一个配置参数的定义
type ConfigParam struct {
	Name        string      `json:"name"`
	Type        string      `json:"type"` // string/number/password/path
	Default     interface{} `json:"default"`
	Description string      `json:"description"`
	Required    bool        `json:"required"`
}

// ServiceConfig 包含服务管理相关配置
type ServiceConfig struct {
	StartCmd        string   `json:"start_cmd"`
	StopCmd         string   `json:"stop_cmd"`
	RestartCmd      string   `json:"restart_cmd"`
	ReloadCmd       string   `json:"reload_cmd"`
	SystemdTemplate string   `json:"systemd_template"`
	EnvFiles        []string `json:"env_files"`
	Requires        []string `json:"requires"`
}

// Version 表示软件版本信息
type Version struct {
	Version         string            `json:"version"`
	DownloadURL     string            `json:"download_url"`
	Checksum        string            `json:"checksum"`
	ConfigTemplates map[string]string `json:"config_templates"`
	Params          []ConfigParam     `json:"params"`
}

func (c *SoftwareClient) GetSoftware(name string) (*Software, error) {
	resp, err := c.httpClient.Get(fmt.Sprintf("%s/software/%s", c.serverURL, name))
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回错误: %s", resp.Status)
	}

	var software Software
	if err := json.NewDecoder(resp.Body).Decode(&software); err != nil {
		return nil, fmt.Errorf("响应解析失败: %v", err)
	}
	return &software, nil
}

// 解析安装路径
func (c *SoftwareClient) resolveInstallPath(software *Software, version string) string {
	return filepath.Join(c.installRoot, software.Name, "v"+version)
}

// 创建目录结构
func (c *SoftwareClient) createDirs(software *Software, version string) error {
	path := c.resolveInstallPath(software, version)
	dirs := []string{
		path,
		filepath.Join(path, "conf"),
		filepath.Join(path, "bin"),
		filepath.Join(c.dataRoot, software.Name, version),
		filepath.Join(c.logRoot, software.Name, version),
	}

	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("创建目录失败 %s: %v", dir, err)
		}
	}
	return nil
}

// 下载和解压软件包
func (c *SoftwareClient) downloadAndExtract(software *Software, version string, targetDir string) error {
	// 获取下载URL
	var downloadURL string
	for _, v := range software.Versions {
		if v.Version == version {
			downloadURL = v.DownloadURL
			break
		}
	}

	// 下载文件
	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("下载失败: %v", err)
	}
	defer resp.Body.Close()

	// 校验文件
	// TODO: 实现校验和验证

	// 解压文件
	switch {
	case strings.HasSuffix(downloadURL, ".tar.gz"):
		return c.extractTarGz(resp.Body, targetDir)
	case strings.HasSuffix(downloadURL, ".zip"):
		return c.extractZip(resp.Body, targetDir)
	default:
		return fmt.Errorf("不支持的压缩格式")
	}
}

// 设置环境变量
func (c *SoftwareClient) setupEnvironment(software *Software, installPath string) error {
	pathEnv := fmt.Sprintf("export PATH=$PATH:%s/bin", installPath)
	envFile := fmt.Sprintf("/etc/profile.d/onesfot-%s.sh", software.Name)
	err := os.WriteFile(envFile, []byte(pathEnv), 0644)
	return err
}

// 获取当前环境变量
func (c *SoftwareClient) getEnvironmentVars() map[string]string {
	envVars := make(map[string]string)
	for _, env := range os.Environ() {
		pair := strings.SplitN(env, "=", 2)
		if len(pair) == 2 {
			envVars[pair[0]] = pair[1]
		}
	}
	return envVars
}

// 解压tar.gz文件
func (c *SoftwareClient) extractTarGz(r io.Reader, targetDir string) error {
	gzr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		header, err := tr.Next()
		switch {
		case err == io.EOF:
			return nil
		case err != nil:
			return err
		case header == nil:
			continue
		}

		target := filepath.Join(targetDir, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if _, err := os.Stat(target); err != nil {
				if err := os.MkdirAll(target, 0755); err != nil {
					return err
				}
			}
		case tar.TypeReg:
			f, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

// 解压zip文件
func (c *SoftwareClient) extractZip(r io.Reader, targetDir string) error {
	// 需要先将io.Reader转换为io.ReaderAt
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}

	for _, file := range zr.File {
		path := filepath.Join(targetDir, file.Name)
		if file.FileInfo().IsDir() {
			os.MkdirAll(path, file.Mode())
			continue
		}

		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, file.Mode())
		if err != nil {
			return err
		}

		rc, err := file.Open()
		if err != nil {
			return err
		}

		_, err = io.Copy(outFile, rc)
		outFile.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
