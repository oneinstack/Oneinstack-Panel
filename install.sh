#!/bin/bash

# Oneinstack Panel 安装脚本 - 优化版本
# Copyright © 2024 oneinstack All rights reserved.

set -euo pipefail  # 严格模式：遇到错误立即退出，未定义变量报错，管道错误传递

# 全局变量
readonly SCRIPT_VERSION="2.0.0"
readonly INSTALL_DIR="/usr/local/one"
readonly LOG_FILE="/tmp/one_install.log"
readonly BACKUP_DIR="/tmp/one_backup_$(date +%Y%m%d_%H%M%S)"

# 颜色定义
readonly RED='\033[31m'
readonly GREEN='\033[32m'
readonly YELLOW='\033[33m'
readonly BLUE='\033[34m'
readonly NC='\033[0m' # No Color

# 日志函数
log() {
    local level="$1"
    shift
    local message="$*"
    local timestamp=$(date '+%Y-%m-%d %H:%M:%S')
    echo -e "[$timestamp] [$level] $message" | tee -a "$LOG_FILE"
}

info() {
    log "INFO" "${BLUE}$*${NC}"
}

warn() {
    log "WARN" "${YELLOW}$*${NC}"
}

error() {
    log "ERROR" "${RED}$*${NC}"
    exit 1
}

success() {
    log "SUCCESS" "${GREEN}$*${NC}"
}

# Logo显示
show_logo() {
    cat << 'EOF'
+----------------------------------------------------
| Oneinstack Panel 安装脚本 v2.0.0
| 安全优化版本
+----------------------------------------------------
| Copyright © 2024 oneinstack All rights reserved.
+----------------------------------------------------
EOF
}

# 检查系统要求
check_system_requirements() {
    info "检查系统要求..."
    
    # 检查root权限
    if [[ $EUID -ne 0 ]]; then
        error "请使用root用户运行此脚本"
    fi
    
    # 检查系统架构
    local arch=$(uname -m)
    case $arch in
        x86_64|amd64)
            info "系统架构: $arch ✓"
            ;;
        *)
            error "不支持的系统架构: $arch"
            ;;
    esac
    
    # 检查内存
    local mem_total=$(free -m | awk 'NR==2{printf "%.0f", $2}')
    if [[ $mem_total -lt 512 ]]; then
        error "系统内存不足，至少需要512MB，当前: ${mem_total}MB"
    fi
    info "系统内存: ${mem_total}MB ✓"
    
    # 检查磁盘空间
    local disk_free=$(df / | awk 'NR==2{printf "%.0f", $4/1024}')
    if [[ $disk_free -lt 1024 ]]; then
        error "磁盘空间不足，至少需要1GB，当前可用: ${disk_free}MB"
    fi
    info "磁盘空间: ${disk_free}MB ✓"
    
    # 检查是否已安装
    if [[ -f "$INSTALL_DIR/one" ]]; then
        warn "检测到面板已安装"
        read -p "是否要重新安装？这将覆盖现有安装 [y/N]: " reinstall
        if [[ ! "$reinstall" =~ ^[Yy]$ ]]; then
            info "安装已取消"
            exit 0
        fi
        backup_existing_installation
    fi
}

# 检测操作系统
detect_os() {
    info "检测操作系统..."
    
    if [[ -f /etc/os-release ]]; then
        source /etc/os-release
        OS_ID="$ID"
        OS_VERSION="$VERSION_ID"
    elif [[ -f /etc/redhat-release ]]; then
        OS_ID="centos"
        OS_VERSION=$(grep -oE '[0-9]+\.[0-9]+' /etc/redhat-release | head -1)
    elif [[ -f /etc/debian_version ]]; then
        OS_ID="debian"
        OS_VERSION=$(cat /etc/debian_version)
    else
        error "无法检测操作系统类型"
    fi
    
    case $OS_ID in
        centos|rhel|rocky|almalinux)
            OS_FAMILY="rhel"
            PACKAGE_MANAGER="yum"
            if command -v dnf >/dev/null 2>&1; then
                PACKAGE_MANAGER="dnf"
            fi
            ;;
        ubuntu|debian)
            OS_FAMILY="debian"
            PACKAGE_MANAGER="apt"
            ;;
        *)
            error "不支持的操作系统: $OS_ID"
            ;;
    esac
    
    info "操作系统: $OS_ID $OS_VERSION ✓"
    info "包管理器: $PACKAGE_MANAGER ✓"
}

# 检测网络环境
detect_network_environment() {
    info "检测网络环境..."
    
    # 检查网络连接
    if ! ping -c 1 -W 5 8.8.8.8 >/dev/null 2>&1; then
        error "网络连接失败，请检查网络设置"
    fi
    
    # 检测是否在中国
    local china_check=$(curl --retry 2 -m 10 -s https://httpbin.org/ip 2>/dev/null | grep -E '"origin".*"(1[0-9]{2}\.|2[0-4][0-9]\.|25[0-5]\.)"' || echo "false")
    if [[ "$china_check" != "false" ]] || curl --retry 2 -m 5 -s https://www.baidu.com >/dev/null 2>&1; then
        IN_CHINA=true
        info "检测到中国网络环境，将使用国内镜像源"
    else
        IN_CHINA=false
        info "检测到海外网络环境"
    fi
}

# 备份现有安装
backup_existing_installation() {
    info "备份现有安装..."
    
    if [[ -d "$INSTALL_DIR" ]]; then
        mkdir -p "$BACKUP_DIR"
        cp -r "$INSTALL_DIR" "$BACKUP_DIR/" 2>/dev/null || true
        info "备份保存至: $BACKUP_DIR"
    fi
}

# 配置系统环境
configure_system() {
    info "配置系统环境..."
    
    # 设置时区
    if command -v timedatectl >/dev/null 2>&1; then
        timedatectl set-timezone Asia/Shanghai
        info "时区设置为: Asia/Shanghai"
    fi
    
    # 配置镜像源（仅在中国环境）
    if [[ "$IN_CHINA" == true ]]; then
        configure_mirrors
    fi
    
    # 创建安装目录
    mkdir -p "$INSTALL_DIR"
    chmod 755 "$INSTALL_DIR"
    
    # 优化系统参数
    configure_system_parameters
}

# 配置镜像源
configure_mirrors() {
    info "配置镜像源..."
    
    case $OS_FAMILY in
        rhel)
            configure_rhel_mirrors
            ;;
        debian)
            configure_debian_mirrors
            ;;
    esac
}

# 配置RHEL系列镜像源
configure_rhel_mirrors() {
    local backup_dir="/etc/yum.repos.d/backup_$(date +%Y%m%d)"
    mkdir -p "$backup_dir"
    cp /etc/yum.repos.d/*.repo "$backup_dir/" 2>/dev/null || true
    
    case $OS_ID in
        centos)
            if [[ "${OS_VERSION%%.*}" -eq 7 ]]; then
                configure_centos7_mirrors
            elif [[ "${OS_VERSION%%.*}" -eq 8 ]]; then
                configure_centos8_mirrors
            fi
            ;;
        rocky|almalinux)
            configure_el8_mirrors
            ;;
    esac
}

# 配置CentOS 7镜像源
configure_centos7_mirrors() {
    cat > /etc/yum.repos.d/CentOS-Base.repo << 'EOF'
[base]
name=CentOS-$releasever - Base - mirrors.aliyun.com
baseurl=https://mirrors.aliyun.com/centos/$releasever/os/$basearch/
gpgcheck=1
gpgkey=https://mirrors.aliyun.com/centos/RPM-GPG-KEY-CentOS-7

[updates]
name=CentOS-$releasever - Updates - mirrors.aliyun.com
baseurl=https://mirrors.aliyun.com/centos/$releasever/updates/$basearch/
gpgcheck=1
gpgkey=https://mirrors.aliyun.com/centos/RPM-GPG-KEY-CentOS-7

[extras]
name=CentOS-$releasever - Extras - mirrors.aliyun.com
baseurl=https://mirrors.aliyun.com/centos/$releasever/extras/$basearch/
gpgcheck=1
gpgkey=https://mirrors.aliyun.com/centos/RPM-GPG-KEY-CentOS-7
EOF
}

# 配置Debian系列镜像源
configure_debian_mirrors() {
    local sources_backup="/etc/apt/sources.list.backup_$(date +%Y%m%d)"
    cp /etc/apt/sources.list "$sources_backup" 2>/dev/null || true
    
    case $OS_ID in
        ubuntu)
            configure_ubuntu_mirrors
            ;;
        debian)
            configure_debian_official_mirrors
            ;;
    esac
}

# 配置Ubuntu镜像源
configure_ubuntu_mirrors() {
    local codename=$(lsb_release -cs 2>/dev/null || echo "jammy")
    cat > /etc/apt/sources.list << EOF
deb https://mirrors.aliyun.com/ubuntu/ $codename main restricted universe multiverse
deb https://mirrors.aliyun.com/ubuntu/ $codename-security main restricted universe multiverse
deb https://mirrors.aliyun.com/ubuntu/ $codename-updates main restricted universe multiverse
deb https://mirrors.aliyun.com/ubuntu/ $codename-backports main restricted universe multiverse
EOF
}

# 安装系统依赖
install_dependencies() {
    info "安装系统依赖..."
    
    case $OS_FAMILY in
        rhel)
            install_rhel_dependencies
            ;;
        debian)
            install_debian_dependencies
            ;;
    esac
}

# 安装RHEL系列依赖
install_rhel_dependencies() {
    local packages=(
        "curl" "wget" "unzip" "zip" "tar"
        "git" "jq" "dos2unix" "make" "sudo"
        "firewalld" "cronie" "logrotate"
        "glibc" "libgcc" "openssl"
    )
    
    $PACKAGE_MANAGER update -y || warn "软件包更新失败，继续安装"
    
    for package in "${packages[@]}"; do
        if ! rpm -q "$package" >/dev/null 2>&1; then
            info "安装: $package"
            $PACKAGE_MANAGER install -y "$package" || warn "安装 $package 失败"
        fi
    done
}

# 安装Debian系列依赖
install_debian_dependencies() {
    local packages=(
        "curl" "wget" "unzip" "zip" "tar"
        "git" "jq" "dos2unix" "make" "sudo"
        "ufw" "cron" "logrotate"
        "libc6" "libgcc-s1" "openssl"
        "ca-certificates"
    )
    
    export DEBIAN_FRONTEND=noninteractive
    apt update -y || warn "软件包更新失败，继续安装"
    
    for package in "${packages[@]}"; do
        if ! dpkg -l "$package" >/dev/null 2>&1; then
            info "安装: $package"
            apt install -y "$package" || warn "安装 $package 失败"
        fi
    done
}

# 配置系统参数
configure_system_parameters() {
    info "配置系统参数..."
    
    # BBR拥塞控制算法
    if [[ $(uname -r | cut -d. -f1) -ge 4 ]] && [[ $(uname -r | cut -d. -f2) -ge 9 ]]; then
        echo 'net.core.default_qdisc=fq' >> /etc/sysctl.conf
        echo 'net.ipv4.tcp_congestion_control=bbr' >> /etc/sysctl.conf
        sysctl -p >/dev/null 2>&1 || warn "BBR配置失败"
        info "BBR拥塞控制已启用"
    fi
    
    # 优化文件描述符限制
    cat >> /etc/security/limits.conf << 'EOF'
* soft nofile 65535
* hard nofile 65535
root soft nofile 65535
root hard nofile 65535
EOF
    
    # 禁用防火墙（临时）
    case $OS_FAMILY in
        rhel)
            systemctl disable firewalld >/dev/null 2>&1 || true
            systemctl stop firewalld >/dev/null 2>&1 || true
            ;;
        debian)
            ufw --force disable >/dev/null 2>&1 || true
            ;;
    esac
}

# 验证下载文件
verify_download() {
    local file="$1"
    local expected_type="$2"
    
    if [[ ! -f "$file" ]]; then
        error "下载文件不存在: $file"
    fi
    
    local file_size=$(stat -c%s "$file" 2>/dev/null || stat -f%z "$file" 2>/dev/null)
    if [[ $file_size -lt 1024 ]]; then
        error "下载文件太小，可能下载失败: $file (${file_size} bytes)"
    fi
    
    local file_type=$(file "$file" 2>/dev/null | tr '[:upper:]' '[:lower:]')
    if [[ ! "$file_type" =~ $expected_type ]]; then
        error "下载文件类型错误: $file_type，期望: $expected_type"
    fi
    
    info "文件验证通过: $file (${file_size} bytes)"
}

# 下载安装包
download_package() {
    info "下载安装包..."
    
    local base_url="https://github.com/oneinstack/panel/releases/latest/download"
    local backup_url="https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack"
    
    # 如果在中国，优先使用备用地址
    if [[ "$IN_CHINA" == true ]]; then
        local temp_url="$base_url"
        base_url="$backup_url"
        backup_url="$temp_url"
    fi
    
    local tarfile="/tmp/one_$(date +%Y%m%d_%H%M%S).tar"
    local download_success=false
    
    # 尝试主要下载地址
    info "尝试从主要地址下载..."
    if curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 300 \
           -o "$tarfile" "${base_url}/one.tar"; then
        verify_download "$tarfile" "tar"
        download_success=true
    else
        warn "主要地址下载失败，尝试备用地址..."
        
        # 尝试备用下载地址
        if curl -fsSL --retry 3 --retry-delay 2 --connect-timeout 10 --max-time 300 \
               -o "$tarfile" "${backup_url}/one.tar"; then
            verify_download "$tarfile" "tar"
            download_success=true
        fi
    fi
    
    if [[ "$download_success" != true ]]; then
        error "所有下载地址均失败，请检查网络连接"
    fi
    
    echo "$tarfile"
}

# 安装面板程序
install_panel() {
    info "安装面板程序..."
    
    local tarfile
    tarfile=$(download_package)
    
    # 解压安装包
    info "解压安装包..."
    if ! tar -xf "$tarfile" -C "$INSTALL_DIR" --strip-components=1; then
        error "解压安装包失败"
    fi
    
    # 清理临时文件
    rm -f "$tarfile"
    
    # 验证核心文件
    local required_files=("one")
    for file in "${required_files[@]}"; do
        if [[ ! -f "$INSTALL_DIR/$file" ]]; then
            error "核心文件缺失: $file"
        fi
    done
    
    # 设置权限
    chmod 755 "$INSTALL_DIR/one"
    chown -R root:root "$INSTALL_DIR"
    
    # 创建符号链接
    ln -sf "$INSTALL_DIR/one" /usr/local/bin/one
    
    success "面板程序安装完成"
}

# 创建系统服务
create_systemd_service() {
    info "创建系统服务..."
    
    cat > /etc/systemd/system/one.service << EOF
[Unit]
Description=Oneinstack Panel Service
Documentation=https://github.com/oneinstack/panel
After=network.target
Wants=network.target

[Service]
Type=simple
User=root
Group=root
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/one server start
ExecStop=$INSTALL_DIR/one server stop
ExecReload=/bin/kill -USR2 \$MAINPID
KillMode=mixed
KillSignal=SIGTERM
TimeoutStopSec=5
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal
SyslogIdentifier=one-panel

# 安全设置
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=$INSTALL_DIR /data /tmp /var/log
PrivateTmp=true
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true

[Install]
WantedBy=multi-user.target
EOF
    
    # 重载systemd配置
    systemctl daemon-reload
    systemctl enable one
    
    success "系统服务创建完成"
}

# 配置日志轮转
configure_log_rotation() {
    info "配置日志轮转..."
    
    cat > /etc/logrotate.d/one << 'EOF'
/usr/local/one/logs/*.log {
    daily
    missingok
    rotate 30
    compress
    delaycompress
    notifempty
    create 644 root root
    sharedscripts
    postrotate
        systemctl reload one >/dev/null 2>&1 || true
    endscript
}
EOF
    
    # 创建日志目录
    mkdir -p "$INSTALL_DIR/logs"
    chmod 755 "$INSTALL_DIR/logs"
}

# 配置环境变量
configure_environment() {
    info "配置环境变量..."
    
    # 添加到PATH
    if ! grep -q "$INSTALL_DIR" /etc/profile; then
        echo "export PATH=\$PATH:$INSTALL_DIR" >> /etc/profile
    fi
    
    # 创建配置目录
    mkdir -p /etc/one
    chmod 755 /etc/one
}

# 初始化面板
initialize_panel() {
    info "初始化面板配置..."
    
    # 生成随机密码
    local admin_password
    admin_password=$(openssl rand -base64 12 | tr -d "=+/" | cut -c1-12)
    
    # 初始化面板
    if ! "$INSTALL_DIR/one" init --user=admin --password="$admin_password" >/dev/null 2>&1; then
        warn "面板初始化失败，请手动初始化"
        return 1
    fi
    
    # 保存初始化信息
    cat > "$INSTALL_DIR/init_info.txt" << EOF
管理员账号: admin
管理员密码: $admin_password
安装时间: $(date '+%Y-%m-%d %H:%M:%S')
安装版本: $SCRIPT_VERSION
EOF
    
    chmod 600 "$INSTALL_DIR/init_info.txt"
    
    info "面板初始化完成"
    echo "$admin_password"
}

# 启动服务
start_services() {
    info "启动面板服务..."
    
    if systemctl start one; then
        success "面板服务启动成功"
    else
        error "面板服务启动失败，请检查日志: journalctl -u one -f"
    fi
    
    # 等待服务启动
    local retry=0
    while [[ $retry -lt 30 ]]; do
        if systemctl is-active one >/dev/null 2>&1; then
            break
        fi
        sleep 1
        ((retry++))
    done
    
    if [[ $retry -ge 30 ]]; then
        warn "服务启动超时，请检查状态"
    fi
}

# 获取访问信息
get_access_info() {
    local server_ip
    server_ip=$(curl -s --connect-timeout 5 --max-time 10 https://httpbin.org/ip 2>/dev/null | grep -o '"[0-9.]*"' | tr -d '"' || echo "localhost")
    
    if [[ -z "$server_ip" ]] || [[ "$server_ip" == "localhost" ]]; then
        server_ip=$(ip route get 8.8.8.8 2>/dev/null | grep -oP 'src \K[^ ]+' | head -1 || echo "localhost")
    fi
    
    local port="8089"  # 默认端口
    
    echo "$server_ip:$port"
}

# 显示安装结果
show_result() {
    local admin_password="$1"
    local access_url
    access_url="http://$(get_access_info)"
    
    clear
    show_logo
    
    cat << EOF

${GREEN}✅ 安装完成！${NC}

+----------------------------------------------------
| 📋 访问信息
+----------------------------------------------------
| 访问地址: ${BLUE}$access_url${NC}
| 管理账号: ${BLUE}admin${NC}
| 管理密码: ${BLUE}$admin_password${NC}
+----------------------------------------------------
| 📋 管理命令
+----------------------------------------------------
| 启动面板: ${YELLOW}systemctl start one${NC}
| 停止面板: ${YELLOW}systemctl stop one${NC}
| 重启面板: ${YELLOW}systemctl restart one${NC}
| 面板状态: ${YELLOW}systemctl status one${NC}
| 查看日志: ${YELLOW}journalctl -u one -f${NC}
+----------------------------------------------------
| 📋 配置文件
+----------------------------------------------------
| 安装目录: ${YELLOW}$INSTALL_DIR${NC}
| 配置文件: ${YELLOW}$INSTALL_DIR/config.yaml${NC}
| 日志目录: ${YELLOW}$INSTALL_DIR/logs${NC}
+----------------------------------------------------

${YELLOW}⚠️  重要提示：${NC}
1. 请及时修改默认密码
2. 建议配置SSL证书
3. 定期备份配置文件
4. 查看完整文档: https://github.com/oneinstack/panel

EOF

    # 保存安装信息到日志
    {
        echo "=========================="
        echo "安装完成时间: $(date)"
        echo "访问地址: $access_url"
        echo "管理账号: admin"
        echo "管理密码: $admin_password"
        echo "=========================="
    } >> "$LOG_FILE"
}

# 清理函数
cleanup() {
    local exit_code=$?
    
    if [[ $exit_code -ne 0 ]]; then
        error "安装过程中发生错误，退出码: $exit_code"
        
        if [[ -d "$BACKUP_DIR" ]]; then
            info "如需恢复，备份位置: $BACKUP_DIR"
        fi
        
        info "详细日志: $LOG_FILE"
    fi
    
    # 清理临时文件
    rm -f /tmp/one_*.tar 2>/dev/null || true
}

# 主安装流程
main() {
    # 设置错误处理
    trap cleanup EXIT
    
    # 创建日志文件
    touch "$LOG_FILE"
    chmod 644 "$LOG_FILE"
    
    # 显示Logo
    clear
    show_logo
    
    # 安装确认
    echo
    read -p "面板将安装到 $INSTALL_DIR 目录，是否继续？[y/N]: " -r confirm
    if [[ ! "$confirm" =~ ^[Yy]$ ]]; then
        info "安装已取消"
        exit 0
    fi
    
    # 执行安装步骤
    info "开始安装 Oneinstack Panel..."
    
    check_system_requirements
    detect_os
    detect_network_environment
    configure_system
    install_dependencies
    install_panel
    create_systemd_service
    configure_log_rotation
    configure_environment
    
    local admin_password
    admin_password=$(initialize_panel)
    
    start_services
    show_result "$admin_password"
    
    success "安装完成！"
}

# 检查是否直接执行
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi