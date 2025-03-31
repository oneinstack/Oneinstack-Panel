#!/bin/bash

# 设置默认变量（可通过环境变量注入覆盖）
db_install_dir='/usr/local/mysql'
db_data_dir='/data/mysql'
CMSG="[INFO]"
CEND="[END]"

# 判断是否安装了 MySQL（或 MariaDB、Percona）
if [ -d "${db_install_dir}/support-files" ]; then
  echo "${CMSG}Stopping MySQL service...${CEND}"
  service mysqld stop > /dev/null 2>&1

  echo "${CMSG}Removing MySQL installation files...${CEND}"
  rm -rf ${db_install_dir} /etc/init.d/mysqld /etc/my.cnf* /etc/ld.so.conf.d/*{mysql,mariadb,percona}*.conf

  echo "${CMSG}Deleting MySQL user...${CEND}"
  id -u mysql >/dev/null 2>&1 && userdel mysql

  # 备份数据目录
  if [ -e "${db_data_dir}" ]; then
    mv "${db_data_dir}" "${db_data_dir}_$(date +%Y%m%d%H%M%S)"
    echo "${CMSG}Data directory moved to backup.${CEND}"
  fi

  # 清理 options.conf 中的密码配置（如存在）
  if [ -f "./options.conf" ]; then
    sed -i 's@^dbrootpwd=.*@dbrootpwd=@' ./options.conf
  fi

  # 清理 /etc/profile 中的 PATH 环境变量
  sed -i "s@${db_install_dir}/bin:@@g" /etc/profile

  echo "${CMSG}MySQL uninstall completed! ${CEND}"
else
  echo "[WARN] MySQL does not seem to be installed in ${db_install_dir}"
fi