#!/bin/bash

# 设置 Redis 安装路径，如果你在 Go 中调用这个脚本，可以通过 ENV 注入这个变量
redis_install_dir='/usr/local/redis'

CMSG="[INFO]"
CEND="[END]"

# 检查是否有 Redis 进程在运行
if [ -n "$(pgrep redis-server)" ]; then
    echo "${CMSG}Stopping Redis server...${CEND}"
    service redis-server stop > /dev/null 2>&1
    systemctl stop redis-server > /dev/null 2>&1
fi

# 停止 Redis 服务并清理安装目录和命令
if [ -e "${redis_install_dir}" ]; then
    echo "${CMSG}Stopping Redis and removing files...${CEND}"
    service redis-server stop > /dev/null 2>&1
    systemctl stop redis-server > /dev/null 2>&1
    rm -rf "${redis_install_dir}" /etc/init.d/redis-server /usr/local/bin/redis-*
    echo "${CMSG}Redis uninstall completed! ${CEND}"
fi

# 清除 systemd 服务配置
if [ -e "/lib/systemd/system/redis-server.service" ]; then
    echo "${CMSG}Disabling systemd service...${CEND}"
    systemctl disable redis-server > /dev/null 2>&1
    rm -f /lib/systemd/system/redis-server.service
fi

# 删除 Redis 用户
id -u redis >/dev/null 2>&1 && userdel redis

# 删除 systemd 环境变量
rm -f /etc/sysconfig/redis*

# 清理 /etc/profile 中的 PATH 环境变量
if [ -f "/etc/profile" ]; then
    sed -i "s@:${redis_install_dir}/bin:@@g" /etc/profile
fi

# 清理 /etc/ld.so.conf.d 中的 Redis 配置
if [ -e "/etc/ld.so.conf.d/redis.conf" ]; then
    rm -f /etc/ld.so.conf.d/redis.conf
fi

# 清理 /etc/sysctl.conf 中的 Redis 配置
if [ -e "/etc/sysctl.conf" ]; then
    sed -i '/vm.overcommit_memory/d' /etc/sysctl.conf
    sysctl -p > /dev/null 2>&1
fi

# 清理 /etc/security/limits.conf 中的 Redis 配置
if [ -e "/etc/security/limits.conf" ]; then
    sed -i '/redis/d' /etc/security/limits.conf
fi

# 清理 /etc/sysconfig/redis 中的 Redis 配置
if [ -e "/etc/sysconfig/redis" ]; then
    rm -f /etc/sysconfig/redis
fi

# 重载systemctl
systemctl daemon-reload

echo "${CMSG}Redis uninstall completed! ${CEND}"