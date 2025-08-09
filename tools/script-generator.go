package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"
)

// ScriptInfo 脚本信息
type ScriptInfo struct {
	SoftwareName    string
	SoftwareVersion string
	AuthorName      string
	AuthorEmail     string
	SupportedOS     string
	UpdateDate      string
	DefaultVersion  string
	DefaultPort     string
	Description     string
	Category        string
}

// 脚本模板
const installScriptTemplate = `#!/bin/bash

#=============================================================================
# {{.SoftwareName}} 安装脚本
# 版本: {{.SoftwareVersion}}
# 作者: {{.AuthorName}} <{{.AuthorEmail}}>
# 描述: {{.Description}}
# 支持系统: {{.SupportedOS}}
# 最后更新: {{.UpdateDate}}
#=============================================================================

set -euo pipefail

# 颜色定义
readonly RED='\033[0;31m'
readonly GREEN='\033[0;32m'
readonly YELLOW='\033[1;33m'
readonly NC='\033[0m'

# 默认配置
readonly DEFAULT_VERSION="{{.DefaultVersion}}"
readonly DEFAULT_PORT="{{.DefaultPort}}"
readonly INSTALL_DIR="/usr/local/{{.SoftwareName}}"

# 全局变量
VERSION=""
PORT=""
PASSWORD=""
DATA_DIR=""

# 日志函数
log() {
    local level=$1
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    
    case $level in
        "INFO")  echo -e "${GREEN}[INFO]${NC} ${timestamp} - $message" ;;
        "WARN")  echo -e "${YELLOW}[WARN]${NC} ${timestamp} - $message" ;;
        "ERROR") echo -e "${RED}[ERROR]${NC} ${timestamp} - $message" ;;
    esac
}

# 显示帮助信息
show_help() {
    cat << EOF
{{.SoftwareName}} 安装脚本

用法: $0 [选项]

选项:
    --version=VERSION    指定安装版本 (默认: ${DEFAULT_VERSION})
    --port=PORT         指定端口 (默认: ${DEFAULT_PORT})
    --password=PASS     设置密码
    --data-dir=DIR      指定数据目录
    --help              显示此帮助信息

示例:
    $0 --version={{.DefaultVersion}} --password=mypass --port={{.DefaultPort}}

EOF
}

# 参数解析
parse_args() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            --version=*)
                VERSION="${1#*=}"
                shift
                ;;
            --port=*)
                PORT="${1#*=}"
                shift
                ;;
            --password=*)
                PASSWORD="${1#*=}"
                shift
                ;;
            --data-dir=*)
                DATA_DIR="${1#*=}"
                shift
                ;;
            --help)
                show_help
                exit 0
                ;;
            *)
                log "ERROR" "未知参数: $1"
                show_help
                exit 1
                ;;
        esac
    done
    
    # 设置默认值
    VERSION=${VERSION:-$DEFAULT_VERSION}
    PORT=${PORT:-$DEFAULT_PORT}
}

# 检查root权限
check_root() {
    if [[ $EUID -ne 0 ]]; then
        log "ERROR" "此脚本需要root权限运行"
        exit 1
    fi
}

# 检测操作系统
detect_os() {
    if [[ -f /etc/os-release ]]; then
        . /etc/os-release
        OS=$ID
        OS_VERSION=$VERSION_ID
        log "INFO" "检测到操作系统: $PRETTY_NAME"
    else
        log "ERROR" "无法检测操作系统类型"
        exit 1
    fi
}

# 安装依赖
install_dependencies() {
    log "INFO" "安装依赖包..."
    
    case $OS in
        ubuntu|debian)
            apt-get update
            apt-get install -y wget curl gnupg2
            # TODO: 添加具体的依赖包
            ;;
        centos|rhel|rocky|almalinux)
            yum install -y wget curl
            # TODO: 添加具体的依赖包
            ;;
        fedora)
            dnf install -y wget curl
            # TODO: 添加具体的依赖包
            ;;
        *)
            log "ERROR" "不支持的操作系统: $OS"
            exit 1
            ;;
    esac
}

# 下载软件
download_software() {
    log "INFO" "下载 {{.SoftwareName}} ${VERSION}..."
    
    # TODO: 实现下载逻辑
    # 示例:
    # local download_url="https://example.com/{{.SoftwareName}}-${VERSION}.tar.gz"
    # wget -O "/tmp/{{.SoftwareName}}-${VERSION}.tar.gz" "$download_url"
    
    log "INFO" "下载完成"
}

# 安装软件
install_software() {
    log "INFO" "安装 {{.SoftwareName}} ${VERSION}..."
    
    # TODO: 实现安装逻辑
    # 示例:
    # cd /tmp
    # tar -xzf {{.SoftwareName}}-${VERSION}.tar.gz
    # cd {{.SoftwareName}}-${VERSION}
    # ./configure --prefix=${INSTALL_DIR}
    # make && make install
    
    log "INFO" "{{.SoftwareName}} 安装完成"
}

# 配置软件
configure_software() {
    log "INFO" "配置 {{.SoftwareName}}..."
    
    # TODO: 实现配置逻辑
    # 示例:
    # mkdir -p ${DATA_DIR}
    # cp config/{{.SoftwareName}}.conf ${INSTALL_DIR}/etc/
    # sed -i "s/PORT_PLACEHOLDER/${PORT}/g" ${INSTALL_DIR}/etc/{{.SoftwareName}}.conf
    
    log "INFO" "{{.SoftwareName}} 配置完成"
}

# 创建systemd服务
create_service() {
    log "INFO" "创建systemd服务..."
    
    cat > /etc/systemd/system/{{.SoftwareName}}.service << EOF
[Unit]
Description={{.SoftwareName}} Server
After=network.target

[Service]
Type=forking
User={{.SoftwareName}}
Group={{.SoftwareName}}
ExecStart=${INSTALL_DIR}/bin/{{.SoftwareName}} -D ${DATA_DIR}
ExecReload=/bin/kill -HUP \$MAINPID
KillMode=mixed
KillSignal=SIGINT
TimeoutSec=0

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable {{.SoftwareName}}
    
    log "INFO" "systemd服务创建完成"
}

# 启动服务
start_service() {
    log "INFO" "启动 {{.SoftwareName}} 服务..."
    
    systemctl start {{.SoftwareName}}
    
    if systemctl is-active --quiet {{.SoftwareName}}; then
        log "INFO" "{{.SoftwareName}} 服务启动成功"
    else
        log "ERROR" "{{.SoftwareName}} 服务启动失败"
        systemctl status {{.SoftwareName}}
        exit 1
    fi
}

# 显示安装信息
show_info() {
    local service_status
    service_status=$(systemctl is-active {{.SoftwareName}} 2>/dev/null || echo "inactive")
    
    cat << EOF

${GREEN}=============================================================================
🎉 {{.SoftwareName}} ${VERSION} 安装完成！
=============================================================================${NC}

${GREEN}📁 安装信息：${NC}
   • 软件版本: ${VERSION}
   • 安装目录: ${INSTALL_DIR}
   • 数据目录: ${DATA_DIR}
   • 配置文件: ${INSTALL_DIR}/etc/{{.SoftwareName}}.conf
   • 端口: ${PORT}
   • 服务状态: ${service_status}

${GREEN}🔧 服务管理：${NC}
   • 启动服务: systemctl start {{.SoftwareName}}
   • 停止服务: systemctl stop {{.SoftwareName}}
   • 重启服务: systemctl restart {{.SoftwareName}}
   • 查看状态: systemctl status {{.SoftwareName}}
   • 查看日志: journalctl -u {{.SoftwareName}} -f

${GREEN}🌐 访问信息：${NC}
   • 本地访问: localhost:${PORT}
   • 配置文件: ${INSTALL_DIR}/etc/{{.SoftwareName}}.conf

${YELLOW}⚠️  重要提示：${NC}
   • 请妥善保管访问凭证
   • 建议配置防火墙规则: ufw allow ${PORT}
   • 定期备份数据目录: ${DATA_DIR}
   • 查看官方文档了解更多配置选项

${GREEN}🔗 相关链接：${NC}
   • 官方网站: https://{{.SoftwareName}}.org
   • 文档: https://{{.SoftwareName}}.org/docs
   • GitHub: https://github.com/{{.SoftwareName}}/{{.SoftwareName}}

EOF
}

# 主函数
main() {
    echo -e "${GREEN}"
    cat << 'EOF'
=============================================================================
                    {{.SoftwareName}} 安装脚本
                    自动安装和配置工具
=============================================================================
EOF
    echo -e "${NC}"
    
    log "INFO" "开始安装 {{.SoftwareName}} ${VERSION}..."
    
    parse_args "$@"
    check_root
    detect_os
    install_dependencies
    download_software
    install_software
    configure_software
    create_service
    start_service
    show_info
    
    log "INFO" "{{.SoftwareName}} 安装完成！"
}

# 执行主函数
main "$@"
`

const configTemplate = `      - name: {{.SoftwareName}}
        display_name: "{{.SoftwareName}}"
        versions: ["{{.DefaultVersion}}"]
        default_version: "{{.DefaultVersion}}"
        description: "{{.Description}}"
        author: "{{.AuthorName}}"
        email: "{{.AuthorEmail}}"
        supported_os: [{{.SupportedOS}}]
        parameters:
          - name: "password"
            type: "string"
            required: true
            description: "管理员密码"
          - name: "port"
            type: "integer"
            default: {{.DefaultPort}}
            description: "服务端口"
          - name: "data_dir"
            type: "string"
            default: "/var/lib/{{.SoftwareName}}"
            description: "数据目录"
        tags: ["{{.Category}}", "{{.SoftwareName}}"]
        category: "{{.Category}}"
        difficulty: "medium"
        estimated_time: "5-10分钟"
`

func main() {
	var (
		name        = flag.String("name", "", "软件名称 (必需)")
		scriptType  = flag.String("type", "install", "脚本类型 (install/uninstall)")
		version     = flag.String("version", "1.0", "软件版本")
		author      = flag.String("author", "", "作者名称")
		email       = flag.String("email", "", "作者邮箱")
		port        = flag.String("port", "8080", "默认端口")
		description = flag.String("desc", "", "软件描述")
		category    = flag.String("category", "other", "软件分类")
		interactive = flag.Bool("i", false, "交互式模式")
	)
	flag.Parse()

	if *name == "" {
		fmt.Println("错误: 必须指定软件名称")
		flag.Usage()
		os.Exit(1)
	}

	scriptInfo := &ScriptInfo{
		SoftwareName:    *name,
		SoftwareVersion: *version,
		AuthorName:      *author,
		AuthorEmail:     *email,
		DefaultVersion:  *version,
		DefaultPort:     *port,
		Description:     *description,
		Category:        *category,
		UpdateDate:      time.Now().Format("2006-01-02"),
		SupportedOS:     `"ubuntu", "debian", "centos", "rhel", "rocky", "almalinux"`,
	}

	// 交互式模式
	if *interactive {
		reader := bufio.NewReader(os.Stdin)

		if scriptInfo.AuthorName == "" {
			fmt.Print("请输入作者名称: ")
			if input, _ := reader.ReadString('\n'); input != "\n" {
				scriptInfo.AuthorName = strings.TrimSpace(input)
			}
		}

		if scriptInfo.AuthorEmail == "" {
			fmt.Print("请输入作者邮箱: ")
			if input, _ := reader.ReadString('\n'); input != "\n" {
				scriptInfo.AuthorEmail = strings.TrimSpace(input)
			}
		}

		if scriptInfo.Description == "" {
			fmt.Printf("请输入 %s 的描述: ", *name)
			if input, _ := reader.ReadString('\n'); input != "\n" {
				scriptInfo.Description = strings.TrimSpace(input)
			}
		}

		fmt.Print("请输入软件分类 (database/webserver/runtime/cache/other): ")
		if input, _ := reader.ReadString('\n'); input != "\n" {
			scriptInfo.Category = strings.TrimSpace(input)
		}

		fmt.Print("请输入默认端口: ")
		if input, _ := reader.ReadString('\n'); input != "\n" {
			if port := strings.TrimSpace(input); port != "" {
				scriptInfo.DefaultPort = port
			}
		}
	}

	// 设置默认值
	if scriptInfo.AuthorName == "" {
		scriptInfo.AuthorName = "Community Contributor"
	}
	if scriptInfo.AuthorEmail == "" {
		scriptInfo.AuthorEmail = "contributor@oneinstack.com"
	}
	if scriptInfo.Description == "" {
		scriptInfo.Description = fmt.Sprintf("自动安装和配置 %s", *name)
	}

	// 创建脚本文件
	if err := createScript(scriptInfo, *scriptType); err != nil {
		fmt.Printf("创建脚本失败: %v\n", err)
		os.Exit(1)
	}

	// 生成配置
	if err := generateConfig(scriptInfo); err != nil {
		fmt.Printf("生成配置失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("✅ 脚本生成成功!\n")
	fmt.Printf("📁 脚本位置: scripts/%s/%s.sh\n", *scriptType, *name)
	fmt.Printf("📝 请编辑脚本实现具体的安装逻辑\n")
	fmt.Printf("⚙️  请将生成的配置添加到 scripts/config.yaml\n")
	fmt.Printf("🧪 测试脚本: go run tools/script-tester.go --script=%s --type=%s\n", *name, *scriptType)
}

func createScript(info *ScriptInfo, scriptType string) error {
	// 确保目录存在
	scriptDir := filepath.Join("scripts", scriptType)
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return fmt.Errorf("创建目录失败: %v", err)
	}

	// 创建脚本文件
	scriptPath := filepath.Join(scriptDir, info.SoftwareName+".sh")
	file, err := os.Create(scriptPath)
	if err != nil {
		return fmt.Errorf("创建文件失败: %v", err)
	}
	defer file.Close()

	// 生成脚本内容
	tmpl, err := template.New("script").Parse(installScriptTemplate)
	if err != nil {
		return fmt.Errorf("解析模板失败: %v", err)
	}

	if err := tmpl.Execute(file, info); err != nil {
		return fmt.Errorf("生成脚本失败: %v", err)
	}

	// 设置可执行权限
	if err := os.Chmod(scriptPath, 0755); err != nil {
		return fmt.Errorf("设置权限失败: %v", err)
	}

	return nil
}

func generateConfig(info *ScriptInfo) error {
	// 生成配置
	tmpl, err := template.New("config").Parse(configTemplate)
	if err != nil {
		return fmt.Errorf("解析配置模板失败: %v", err)
	}

	configPath := fmt.Sprintf("scripts/config-%s.yaml", info.SoftwareName)
	file, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("创建配置文件失败: %v", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, info); err != nil {
		return fmt.Errorf("生成配置失败: %v", err)
	}

	fmt.Printf("\n📋 生成的配置 (请添加到 scripts/config.yaml):\n")
	fmt.Println("---")

	// 显示生成的配置内容
	file.Seek(0, 0)
	content := make([]byte, 1024*4)
	n, _ := file.Read(content)
	fmt.Print(string(content[:n]))
	fmt.Println("---")

	return nil
}
