#!/bin/bash
export PATH=/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin:$PATH

LOGO="+----------------------------------------------------\n| one面板安装脚本 (CentOS)\n| \n+----------------------------------------------------\n| Copyright © 2022-"$(date +%Y)" oneinstack All rights reserved.\n+----------------------------------------------------"
current_path=$(pwd)
in_china=$(curl --retry 2 -m 10 -L https://www.qualcomm.cn/cdn-cgi/trace 2>/dev/null | grep -qx 'loc=CN' && echo "true" || echo "false")

# 检查系统
Prepare_System() {
    if [ $(whoami) != "root" ]; then
        error "请使用root用户运行安装命令(Please run the installation command using the root user)"
    fi

    if [ -f "/usr/local/one/one" ]; then
        error "面板已安装，无需重复安装(one is already installed, no need to install again)"
    fi

    if [ ! -d /usr/local/one ]; then
        mkdir /usr/local/one
    fi

    timedatectl set-timezone Asia/Shanghai
    
    # 添加阿里云CentOS镜像源
    if [ "$in_china" = "true" ]; then
        sed -e 's|^mirrorlist=|#mirrorlist=|g' \
            -e 's|^#baseurl=http://mirror.centos.org|baseurl=https://mirrors.aliyun.com|g' \
            -i.bak \
            /etc/yum.repos.d/CentOS-*.repo
    fi

    yum update -y
    yum install -y curl wget zip unzip tar p7zip p7zip-plugins git jq git-core dos2unix make sudo firewalld crontab
    
    # 安装基础库
    yum install -y glibc glibc-common libgcc libc6

    # 启用 ufw 并设置规则
    ufw disable

    # 安装 BBR
    sysctl -w net.ipv4.tcp_congestion_control=bbr
    sysctl -w net.core.default_qdisc=fq
}

Install_One() {
    local url="https://cdn.bugotech.com/oneinstack/one"
    local dest="/usr/local/one/one"
    local timeout=30  # 设置下载超时时间为30秒

    # 下载 one 二进制文件，设置超时和等待时间
    curl --max-time $timeout -L -o "$dest" "$url"
    chmod +x "$dest"

    # 创建 systemd 服务文件
    cat <<EOF > /etc/systemd/system/one.service
[Unit]
Description=One Service

[Service]
ExecStart=$dest server start
ExecStop=$dest server stop

[Install]
WantedBy=multi-user.target
EOF

    systemctl daemon-reload
    systemctl enable one
    systemctl start one

    # 添加到 PATH
    if ! grep -q "/usr/local/one" /etc/profile; then
        echo "export PATH=\$PATH:/usr/local/one" >> /etc/profile
    fi
}

clear
echo -e $LOGO

# 安装确认
read -p "面板将安装至 /usr/local/one 目录，请输入 y 并回车以开始安装 (Enter 'y' to start installation): " install
if [ "$install" != 'y' ]; then
    echo "输入不正确，已退出安装。"
    exit
fi

clear
echo -e $LOGO
echo "安装面板依赖软件"
Prepare_System

clear
echo -e $LOGO
echo "安装面板运行环境"
Install_One

clear
echo -e $LOGO
echo -e "\n\n面板安装成功！\n+----------------------------------------------------"
echo "服务状态："
systemctl status one --no-pager
echo -e "\n最近日志："
journalctl -u one --no-pager -n 100 --since "5 min ago" -o cat
echo -e "+----------------------------------------------------\n提示：后续查看日志可使用 journalctl -u one -f"
cd ${current_path}
rm -f install_centos.sh
