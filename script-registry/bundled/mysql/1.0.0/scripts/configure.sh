#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
emit_progress 8 prepare_directories "正在准备 MySQL 数据目录"
install -d -o mysql -g mysql -m 0750 -- "${data_dir}"
emit_progress 20 write_config "正在写入 MySQL 配置"
cat >"${config_file}" <<EOF
[client]
port=${mysql_port}
socket=/run/mysqld/mysqld.sock
default-character-set=utf8mb4
[mysqld]
user=mysql
basedir=${install_dir}
datadir=${data_dir}
port=${mysql_port}
bind-address=${bind_address}
socket=/run/mysqld/mysqld.sock
pid-file=/run/mysqld/mysqld.pid
log-error=${data_dir}/mysql-error.log
mysqlx=0
skip-name-resolve
character-set-server=utf8mb4
collation-server=utf8mb4_0900_ai_ci
default-time-zone=+08:00
max_connections=300
open_files_limit=65535
[mysqldump]
quick
max_allowed_packet=64M
EOF
chmod 0644 "${config_file}"
emit_progress 40 write_service "正在写入 MySQL systemd 服务"
cat >"${unit_file}" <<EOF
[Unit]
Description=MySQL Community Server
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=mysql
Group=mysql
RuntimeDirectory=mysqld
RuntimeDirectoryMode=0755
ExecStart=${install_dir}/bin/mysqld --defaults-file=${config_file}
Restart=on-failure
RestartSec=5
TimeoutStartSec=900
LimitNOFILE=65535
PrivateTmp=true
[Install]
WantedBy=multi-user.target
EOF
new_database=false
if [[ ! -d "${data_dir}/mysql" ]]; then
  [[ -n "${mysql_password}" ]] || die "MYSQL_PASSWORD is required for a new database."
  emit_progress 55 initialize_database "正在初始化 MySQL 数据目录"
  "${install_dir}/bin/mysqld" --defaults-file="${config_file}" --initialize-insecure --user=mysql
  : >"${state_dir}/initialized-this-run"
  new_database=true
fi
chown -R mysql:mysql "${data_dir}"
emit_progress 72 service_start "正在启动 MySQL 服务"
systemctl daemon-reload; systemctl enable --now mysql
if [[ "${new_database}" == "true" ]]; then
  client_file="$(mktemp "${state_dir}/client.XXXXXX")"
  trap 'rm -f -- "${client_file}"' EXIT
  chmod 0600 "${client_file}"
  cat >"${client_file}" <<EOF
[client]
user=root
socket=/run/mysqld/mysqld.sock
EOF
  emit_progress 88 secure_database "正在设置 MySQL 管理账户"
  "${install_dir}/bin/mysql" --defaults-extra-file="${client_file}" \
    --execute="ALTER USER 'root'@'localhost' IDENTIFIED BY '${mysql_password}'; DELETE FROM mysql.user WHERE User=''; FLUSH PRIVILEGES;"
fi
emit_progress 100 configure_completed "MySQL 配置和服务部署完成"
echo "MySQL configuration and systemd service installed."
