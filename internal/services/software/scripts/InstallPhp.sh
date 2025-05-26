#!/bin/bash

# 检查操作系统类型并选择安装方法
OS=$(awk -F= '/^NAME/{print $2}' /etc/os-release)

echo "操作系统: $OS"

# 设置稳定版本的 PHP 版本
case "$1" in
    5)
        PHP_VERSION="5.6"
        ;;
    7)
        PHP_VERSION="7.4"
        ;;
    8)
        PHP_VERSION="8.1"
        ;;
    *)
        echo "请提供有效的 PHP 版本 (5, 7, 或 8)。例如: ./php.sh 5"
        exit 1
        ;;
esac

# 更新包管理器的仓库
echo "更新包列表..."
if [[ "$OS" =~ "Ubuntu" || "$OS" =~ "Debian" ]]; then
    sudo apt update -y
    sudo apt install -y software-properties-common

    # 添加 PPA 仓库，支持多个 PHP 版本
    sudo add-apt-repository -y ppa:ondrej/php
    sudo apt update -y

    # 安装指定版本的 PHP 及常用扩展
    echo "安装 PHP $PHP_VERSION 和相关扩展..."
    sudo apt install -y php$PHP_VERSION php$PHP_VERSION-cli php$PHP_VERSION-fpm php$PHP_VERSION-mysql php$PHP_VERSION-xml php$PHP_VERSION-mbstring php$PHP_VERSION-curl php$PHP_VERSION-gd php$PHP_VERSION-zip
elif [[ "$OS" =~ "CentOS" || "$OS" =~ "RHEL" ]]; then
    sudo yum update -y
    sudo yum install -y epel-release
    sudo yum install -y https://rpms.remirepo.net/enterprise/remi-release-7.rpm
    sudo yum install -y yum-utils

    # 启用 Remi 仓库并安装指定版本 PHP
    echo "安装 PHP $PHP_VERSION 和相关扩展..."
    sudo yum module enable -y php:$PHP_VERSION
    sudo yum install -y php$PHP_VERSION php$PHP_VERSION-cli php$PHP_VERSION-fpm php$PHP_VERSION-mysqlnd php$PHP_VERSION-xml php$PHP_VERSION-mbstring php$PHP_VERSION-curl php$PHP_VERSION-gd php$PHP_VERSION-zip
else
    echo "不支持的操作系统。只支持 Ubuntu/Debian/CentOS/RHEL。"
    exit 1
fi

# 启动 PHP-FPM 服务并设置为开机自启
echo "启动 PHP $PHP_VERSION FPM 服务..."
if [[ "$OS" =~ "Ubuntu" || "$OS" =~ "Debian" ]]; then
    sudo systemctl start php$PHP_VERSION-fpm
    sudo systemctl enable php$PHP_VERSION-fpm
elif [[ "$OS" =~ "CentOS" || "$OS" =~ "RHEL" ]]; then
    sudo systemctl start php-fpm
    sudo systemctl enable php-fpm
fi

# 检查 PHP 安装
echo "检查 PHP $PHP_VERSION 版本..."
php -v

# 提示安装完成
echo "PHP $PHP_VERSION 安装完成，FPM 服务已启动并设置为开机自启。"