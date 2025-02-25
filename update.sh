#!/bin/bash
export PATH=/bin:/sbin:/usr/bin:/usr/sbin:/usr/local/bin:/usr/local/sbin:$PATH

LOGO="+----------------------------------------------------\n| one面板更新脚本\n| \n+----------------------------------------------------\n| Copyright © 2022-"$(date +%Y)" oneinstack All rights reserved.\n+----------------------------------------------------"
current_path=$(pwd)
in_china=$(curl --retry 2 -m 10 -L https://www.qualcomm.cn/cdn-cgi/trace 2>/dev/null | grep -qx 'loc=CN' && echo "true" || echo "false")

# 错误处理
error() {
    echo -e "\033[31m错误: $1\033[0m"
    exit 1
}

# 更新核心文件
Update_Core() {
    local url="https://cdn.bugotech.com/oneinstack/one"
    local tarfile="/tmp/one_update_$(date +%s).tar"
    local timeout=30
    local max_retries=3
    local retry_count=0

    # 带重试机制的下载
    while [ $retry_count -lt $max_retries ]; do
        echo "下载更新包(尝试 $((retry_count+1))/$max_retries)..."
        if curl --max-time $timeout -L -o "$tarfile" "${url}.tar"; then
            break
        else
            retry_count=$((retry_count+1))
            rm -f "$tarfile"
            sleep 2
        fi
    done
    
    [ $retry_count -eq $max_retries ] && error "下载失败，已达最大重试次数"

    # 验证文件类型
    file "$tarfile" | grep -q 'tar archive' || error "更新包格式错误"

    echo "更新核心程序..."
    tar --overwrite -xvf "$tarfile" -C /usr/local/one/ one || error "解压失败"
    
    # 验证文件
    [ -f /usr/local/one/one ] || error "核心文件缺失"
    
    # 设置权限
    chmod +x /usr/local/one/one
    rm -f "$tarfile"
}

clear
echo -e $LOGO

# 执行更新
Update_Core

echo -e "\n+----------------------------------------------------"
echo -e "文件更新完成！\n如需应用更新，请手动重启服务："
echo -e "systemctl restart one"
echo -e "\n提示：查看实时日志可使用 journalctl -u one -f"
cd ${current_path}
rm -f update.sh