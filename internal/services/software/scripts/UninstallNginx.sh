#!/bin/bash

# 可根据实际安装路径修改
nginx_install_dir="/usr/local/nginx"
tengine_install_dir="/usr/local/tengine"
openresty_install_dir="/usr/local/openresty"

# 控制台提示颜色变量（可选）
CMSG="[Info]"
CEND="\033[0m"

Uninstall_Nginx() {
  # 卸载 Nginx
  if [ -d "${nginx_install_dir}" ]; then
    echo "${CMSG}正在卸载 Nginx...${CEND}"
    killall nginx > /dev/null 2>&1
    rm -rf ${nginx_install_dir} /etc/init.d/nginx /etc/logrotate.d/nginx
    sed -i "s@${nginx_install_dir}/sbin:@@g" /etc/profile
    echo "${CMSG}Nginx 卸载完成！${CEND}"
  fi

  # 卸载 Tengine
  if [ -d "${tengine_install_dir}" ]; then
    echo "${CMSG}正在卸载 Tengine...${CEND}"
    killall nginx > /dev/null 2>&1
    rm -rf ${tengine_install_dir} /etc/init.d/nginx /etc/logrotate.d/nginx
    sed -i "s@${tengine_install_dir}/sbin:@@g" /etc/profile
    echo "${CMSG}Tengine 卸载完成！${CEND}"
  fi

  # 卸载 OpenResty
  if [ -d "${openresty_install_dir}" ]; then
    echo "${CMSG}正在卸载 OpenResty...${CEND}"
    killall nginx > /dev/null 2>&1
    rm -rf ${openresty_install_dir} /etc/init.d/nginx /etc/logrotate.d/nginx
    sed -i "s@${openresty_install_dir}/nginx/sbin:@@g" /etc/profile
    echo "${CMSG}OpenResty 卸载完成！${CEND}"
  fi

  # systemd 清理
  if [ -e "/lib/systemd/system/nginx.service" ]; then
    systemctl disable nginx > /dev/null 2>&1
    rm -f /lib/systemd/system/nginx.service
  fi

  # 重载systemctl
  systemctl daemon-reload

  echo "${CMSG}Nginx 系列卸载完毕${CEND}"
  source /etc/profile
}

Uninstall_Nginx