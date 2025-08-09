package website

import (
	"fmt"
	"oneinstack/internal/models"
	"oneinstack/utils"
	"os"
	"strings"
)

var (
	tps = map[string]string{
		"php": `
# 自动生成的 PHP 配置 - %s

server {
    listen 80;
    server_name %s;

    root %s;
    index index.php;

    location ~ \.php$ {
        try_files $uri =404;
        fastcgi_pass 127.0.0.1:9000;
        fastcgi_index index.php;
        fastcgi_param SCRIPT_FILENAME %s$document_root$fastcgi_script_name;
        include fastcgi_params;
    }

    # 备注
    # %s

    access_log /data/wwwlogs/%s_access.log;
    error_log /data/wwwlogs/%s_error.log;
}`,

		"proxy": `

# 自动生成的反向代理配置 - %s

server {
    listen 80;
    server_name %s;

    location / {
        proxy_pass %s://%s;
        proxy_set_header Host %s;   # 使用 TarUrl 字段作为 Host
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_cache_bypass $http_upgrade;
    }

    # 备注
    # %s

    access_log /data/wwwlogs/%s_access.log;
    error_log /data/wwwlogs/%s_error.log;

}`,
		"static": `
# 自动生成的静态文件服务配置 - %s
server {
    listen 80;
    server_name %s;

    root %s;
    index index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }

    # 备注
    # %s

    access_log /data/wwwlogs/%s_access.log;
    error_log /data/wwwlogs/%s_error.log;
}
`,
	}
)

// GenerateNginxConfig 生成 Nginx 配置的函数
func GenerateNginxConfig(p *models.Website) (string, error) {
	config, err := GetNginxConfig(p)
	if err != nil {
		return "", err
	}
	err = SaveConfigToFile(config, p)
	if err != nil {
		return "", err
	}
	return config, nil
}

func GetNginxConfig(p *models.Website) (string, error) {
	config := ""
	switch p.Type {
	case "php":
		config = fmt.Sprintf(tps["php"], p.Name, p.Name, "/data/wwwroot/"+p.Dir, "/data/wwwroot/"+p.Dir, p.Remark, p.Name, p.Name)
	case "proxy":
		config = fmt.Sprintf(tps["proxy"], p.Name, p.Name, p.Pact, p.SendUrl, p.TarUrl, p.Remark, p.Name, p.Name)
	case "static":
		config = fmt.Sprintf(tps["static"], p.Name, p.Name, "/data/wwwroot/"+p.Dir, p.Remark, p.Name, p.Name)
	}
	return config, nil
}

// UpdateConfigIfExists 更新配置文件，如果已存在则更新它
func UpdateConfigIfExists(filePath, newConfig string) error {
	// 检查配置文件是否存在
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// 如果文件不存在，直接创建新文件
		return os.WriteFile(filePath, []byte(newConfig), 0644)
	}

	// 如果文件存在，打开文件并更新
	fileContent, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// 检查配置文件中是否已有此网站配置
	if strings.Contains(string(fileContent), newConfig) {
		fmt.Println("Configuration already exists. No changes needed.")
		return nil
	}

	// 文件内容中未找到配置，直接更新文件
	fileContent = append(fileContent, []byte("\n"+newConfig)...)
	return os.WriteFile(filePath, fileContent, 0644)
}

// CreateSymlink 创建软链接，如果目标文件已存在则删除后重新创建
func CreateSymlink(source, target string) error {
	// 检查目标文件是否已经存在
	if _, err := os.Stat(target); err == nil {
		// 如果存在，删除它
		err := os.Remove(target)
		if err != nil {
			return fmt.Errorf("failed to remove existing file %s: %v", target, err)
		}
	}

	// 创建新的软链接
	err := os.Symlink(source, target)
	if err != nil {
		return fmt.Errorf("failed to create symlink %s -> %s: %v", source, target, err)
	}
	return nil
}

// SaveConfigToFile 保存新的配置文件，并创建软链接
func SaveConfigToFile(config string, p *models.Website) error {
	// 定义目录路径
	nginxSitesAvailableDir := "/usr/local/nginx/conf/vhost"
	logDir := "/usr/local/nginx/log"

	// 检查并创建相关目录
	directories := []string{nginxSitesAvailableDir, logDir}
	for _, dir := range directories {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %v", dir, err)
		}
	}

	// 创建一个文件路径
	filePath := fmt.Sprintf("%s/%s", nginxSitesAvailableDir, p.Name+".conf")

	// 更新或创建配置文件
	err := UpdateConfigIfExists(filePath, config)
	if err != nil {
		return err
	}

	return nil
}

// ReloadNginxConfig 尝试重载 Nginx 配置
func ReloadNginxConfig() error {
	// 检查 Nginx 是否已安装
	safeCmd := utils.NewSafeCommand("nginx", "-v")
	_, err := safeCmd.Execute()
	if err != nil {
		return fmt.Errorf("Nginx is not installed or not found in PATH")
	}

	// 重载 Nginx 配置
	reloadCmd := utils.NewSafeCommand("nginx", "-s", "reload")
	_, err = reloadCmd.Execute()
	if err != nil {
		return fmt.Errorf("failed to reload Nginx config: %v", err)
	}

	return nil
}

// DeleteNginxConfig 删除指定的 Nginx 配置文件及符号链接
func DeleteNginxConfig(websiteName string) error {
	// 配置文件路径
	availablePath := fmt.Sprintf("/usr/local/nginx/conf/vhost/%s", websiteName)

	pathFile := availablePath + ".conf"
	_, err := os.Stat(pathFile)
	if os.IsNotExist(err) { // 文件不存在则不需要删除，直接返回
		return nil
	}
	// 删除 sites-available 中的配置文件
	err = os.Remove(pathFile)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("config file %s not found in sites-available", availablePath)
		}
		return fmt.Errorf("failed to delete config file %s: %v", availablePath, err)
	}
	return nil
}
