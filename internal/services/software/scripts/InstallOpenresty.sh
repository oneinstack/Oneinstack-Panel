#!/bin/bash

# 安装重试次数
MAX_RETRIES=3
# 重试间隔时间（秒）
RETRY_DELAY=5

# 颜色定义
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m' # No Color

# 检查root权限
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}错误：请使用root权限或sudo运行此脚本${NC}"
    exit 1
fi

# 带重试功能的命令执行函数
retry_command() {
    local command="$1"
    local description="$2"
    local attempt=1

    until eval "$command"; do
        if [ $attempt -ge $MAX_RETRIES ]; then
            echo -e "${RED}$description 失败，已达到最大重试次数${NC}"
            return 1
        fi
        echo -e "${YELLOW}$description 失败，${attempt}/${MAX_RETRIES} 重试...${NC}"
        sleep $RETRY_DELAY
        ((attempt++))
    done
    return 0
}

# 检测系统类型
if [ -f /etc/os-release ]; then
    . /etc/os-release
    OS=$ID
    VERSION=$VERSION_ID
else
    echo -e "${RED}无法检测操作系统类型${NC}"
    exit 1
fi

# 安装必要工具
install_tools() {
    case "$OS" in
        ubuntu|debian)
            retry_command "apt-get update" "更新包列表" && \
            retry_command "apt-get install -y wget gnupg" "安装依赖工具"
            ;;
        centos|almalinux|fedora)
            retry_command "yum install -y wget" "安装依赖工具"
            ;;
        *)
            echo -e "${RED}不支持的Linux发行版: $OS${NC}"
            exit 1
            ;;
    esac
}

# 添加OpenResty仓库
add_repo() {
    case "$OS" in
        ubuntu|debian)
            if [ ! -f /etc/apt/sources.list.d/openresty.list ]; then
                retry_command "wget -qO - https://openresty.org/package/pubkey.gpg | apt-key add -" "导入GPG密钥" && \
                retry_command "echo \"deb http://openresty.org/package/ubuntu $(lsb_release -sc) main\" > /etc/apt/sources.list.d/openresty.list" "添加APT仓库"
            fi
            ;;
        centos|almalinux)
            if [ ! -f /etc/yum.repos.d/openresty.repo ]; then
                retry_command "wget -qO /etc/yum.repos.d/openresty.repo https://openresty.org/package/centos/openresty.repo" "添加YUM仓库"
            fi
            ;;
        fedora)
            if [ ! -f /etc/yum.repos.d/openresty.repo ]; then
                retry_command "wget -qO /etc/yum.repos.d/openresty.repo https://openresty.org/package/fedora/openresty.repo" "添加Fedora仓库"
            fi
            ;;
    esac
}

# 执行安装流程
install_openresty() {
    echo -e "${GREEN}开始安装OpenResty...${NC}"

    # 安装依赖工具
    if ! install_tools; then
        echo -e "${RED}依赖工具安装失败${NC}"
        exit 1
    fi

    # 添加仓库
    if ! add_repo; then
        echo -e "${RED}仓库配置失败${NC}"
        exit 1
    fi

    # 安装OpenResty
    case "$OS" in
        ubuntu|debian)
            retry_command "apt-get update" "更新包列表" && \
            retry_command "apt-get install -y openresty" "安装OpenResty"
            ;;
        centos|almalinux|fedora)
            retry_command "yum install -y openresty" "安装OpenResty"
            ;;
    esac

    if [ $? -eq 0 ]; then
        echo -e "${GREEN}OpenResty 安装成功！${NC}"
        echo -e "运行命令启动服务: systemctl start openresty"
    else
        echo -e "${RED}OpenResty 安装失败${NC}"
        exit 1
    fi
}

# 主执行流程
install_openresty
