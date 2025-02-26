#!/bin/bash
export PATH=/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin:$PATH

LOGO="+----------------------------------------------------\n| one面板安装脚本\n| \n+----------------------------------------------------\n| Copyright © 2022-"$(date +%Y)" oneinstack All rights reserved.\n+----------------------------------------------------"
current_path=$(pwd)
in_china=$(curl --retry 2 -m 10 -L https://www.qualcomm.cn/cdn-cgi/trace 2>/dev/null | grep -qx 'loc=CN' && echo "true" || echo "false")

# 检测操作系统
Detect_OS() {
    if [ -f /etc/redhat-release ]; then
        OS="centos"
    elif [ -f /etc/lsb-release ] || [ -f /etc/debian_version ]; then
        OS="ubuntu"
    else
        error "不支持的操作系统(Unsupported OS)"
    fi
}

# 系统准备
Prepare_System() {
    if [ $(whoami) != "root" ]; then
        error "请使用root用户运行安装命令(Please run the installation command using the root user)"
    fi

    if [ -f "/usr/local/one/one" ]; then
        error "面板已安装，无需重复安装(one is already installed, no need to install again)"
    fi

    [ ! -d /usr/local/one ] && mkdir /usr/local/one

    timedatectl set-timezone Asia/Shanghai

    # 配置镜像源
    if [ "$in_china" = "true" ]; then
        case $OS in
            centos)
                sed -e 's|^mirrorlist=|#mirrorlist=|g' \
                    -e 's|^#baseurl=http://mirror.centos.org|baseurl=https://mirrors.aliyun.com|g' \
                    -i.bak \
                    /etc/yum.repos.d/CentOS-*.repo
                ;;
            ubuntu)
                cp /etc/apt/sources.list /etc/apt/sources.list.bak
                cat > /etc/apt/sources.list << EOF
deb http://mirrors.aliyun.com/ubuntu/ jammy main restricted universe multiverse
deb http://mirrors.aliyun.com/ubuntu/ jammy-security main restricted universe multiverse
deb http://mirrors.aliyun.com/ubuntu/ jammy-updates main restricted universe multiverse
deb http://mirrors.aliyun.com/ubuntu/ jammy-proposed main restricted universe multiverse
deb http://mirrors.aliyun.com/ubuntu/ jammy-backports main restricted universe multiverse
EOF
                ;;
        esac
    fi

    # 安装基础软件
    case $OS in
        centos)
            yum update -y
            yum install -y curl wget unzip zip tar p7zip p7zip-plugins git jq git-core dos2unix make sudo firewalld crontab 
            yum install -y glibc glibc-common libgcc libc6
            ;;
        ubuntu)
            apt update -y
            apt install -y libc6 libc-bin curl wget unzip zip p7zip p7zip-full git jq git dos2unix make sudo ufw crontab 
            ;;
    esac

    # 公共配置
    ufw disable
    sysctl -w net.ipv4.tcp_congestion_control=bbr
    sysctl -w net.core.default_qdisc=fq
}

Install_One() {
    local url="https://bugo-1301111475.cos.ap-guangzhou.myqcloud.com/oneinstack/one"
    local tarfile="/tmp/one.tar"
    local timeout=30

    # 下载并解压
    echo "正在下载安装包..."
    curl -k --max-time $timeout -L -o "$tarfile" "${url}.tar" || error "下载失败"
    
    # 验证文件类型
    if ! file "$tarfile" | grep -q 'tar archive'; then
        error "安装包格式错误，请检查下载源"
    fi
    
    echo "解压安装文件..."
    tar -vxf "$tarfile" -C /usr/local/one/  || error "解压失败"
    
    # 验证必要文件
    if [ ! -f /usr/local/one/one ]; then
        error "核心文件缺失，请检查安装包完整性"
    fi
    rm -f "$tarfile"
    # 设置可执行权限
    chmod +x /usr/local/one/one
    chmod +x /usr/local/one/tools/* 2>/dev/null

    # 初始化日志文件
    touch /usr/local/one/info.log
    chmod 644 /usr/local/one/info.log
    chown root:root /usr/local/one/info.log

    # 创建 systemd 服务
    cat <<EOF > /etc/systemd/system/one.service
[Unit]
Description=One Service

[Service]
ExecStart=/usr/local/one/one server start
StandardOutput=file:/usr/local/one/info.log
StandardError=inherit
ExecStop=/usr/local/one/one server stop

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable one
    systemctl start one

    # 添加日志轮转
    cat <<EOF > /etc/logrotate.d/one
+/usr/local/one/info.log {
    daily
    missingok
    rotate 7
    compress
    delaycompress
    notifempty
    create 644 root root
}
EOF

    # 添加PATH
    grep -q "/usr/local/one" /etc/profile || echo "export PATH=\$PATH:/usr/local/one" >> /etc/profile
}

error() {
    echo -e "\033[31m错误: $1\033[0m"
    exit 1
}

clear
echo -e $LOGO

# 安装确认
read -p "面板将安装至 /usr/local/one 目录，请输入 y 并回车以开始安装 (Enter 'y' to start installation): " install
[ "$install" != 'y' ] && echo "输入不正确，已退出安装。" && exit

clear
echo -e $LOGO
echo "检测系统环境..."
Detect_OS

clear
echo -e $LOGO
echo "安装依赖软件 ($OS)"
Prepare_System

clear
echo -e $LOGO
echo "安装面板服务"
Install_One

clear
echo -e $LOGO
echo -e "\n\n面板安装成功！\n+----------------------------------------------------"
echo "服务状态：创建默认配置文件"
systemctl status one --no-pager
sleep 5
if [ -f /usr/local/one/info.log ]; then
    cat /usr/local/one/info.log | grep '用户'
    cat /usr/local/one/info.log | grep '访问'
else
    echo "日志文件尚未生成，请稍后查看 /usr/local/one/info.log"
fi
echo -e "+----------------------------------------------------\n提示：后续查看日志可使用 journalctl -u one -f"
cd ${current_path}
rm -f install.sh 