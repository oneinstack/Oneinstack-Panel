#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; ensure_account
emit_progress 8 prepare_directories "正在准备 MySQL 数据目录"
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
ensure_default_data_root_access
verify_runtime_permissions
migrate_external_mysql_data
emit_progress 20 write_config "正在写入 MySQL 配置"
if [[ ! -f "${config_file}" || ! -f "${state_dir}/version" ]]; then
  candidate_config="$(mktemp "$(dirname -- "${config_file}")/.oneinstack-mycnf.XXXXXX")"
  chmod 0640 "${candidate_config}"
  cat >"${candidate_config}" <<EOF
[client]
port=${mysql_port}
socket=/run/mysqld/mysqld.sock
default-character-set=utf8mb4
[mysqld]
user=${run_user}
basedir=${install_dir}
datadir=${data_dir}
port=${mysql_port}
bind-address=${bind_address}
socket=/run/mysqld/mysqld.sock
pid-file=/run/mysqld/mysqld.pid
log-error=${log_dir}/mysql-error.log
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
  chown root:"${run_group}" "${candidate_config}"
  mv -f -- "${candidate_config}" "${config_file}"
fi
emit_progress 40 write_service "正在写入 MySQL systemd 服务"
if has_systemd; then
cat >"${unit_file}" <<EOF
[Unit]
Description=MySQL Community Server
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=${run_user}
Group=${run_group}
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
  chmod 0644 "${unit_file}"
fi
new_database=false
if [[ ! -d "${data_dir}/mysql" ]]; then
  [[ -n "${mysql_password}" ]] || die "MYSQL_PASSWORD is required for a new database."
  emit_progress 55 initialize_database "正在初始化 MySQL 数据目录"
  "${install_dir}/bin/mysqld" --defaults-file="${config_file}" --initialize-insecure --user="${run_user}"
  : >"${state_dir}/initialized-this-run"
  new_database=true
fi
chown -R "${run_user}:${run_group}" "${data_dir}" "${log_dir}"
normalize_runtime_permissions
emit_progress 72 service_start "正在启动 MySQL 服务"
service_start
emit_progress 78 service_ready "正在等待 MySQL 服务就绪"
mysql_ready=false
for _ in $(seq 1 120); do
  if [[ -S /run/mysqld/mysqld.sock ]]; then
    mysql_ready=true
    break
  fi
  service_is_active ||
    die "MySQL service exited before becoming ready."
  sleep 1
done
[[ "${mysql_ready}" == "true" ]] ||
  die "MySQL socket was not ready within 120 seconds."
client_file=""
login_path_file="$(mktemp "${state_dir}/login-path.XXXXXX")"
trap '[[ -z "${client_file}" ]] || rm -f -- "${client_file}"; rm -f -- "${login_path_file}"' EXIT
chmod 0600 "${login_path_file}"
if [[ "${new_database}" == "true" ]]; then
  client_file="$(mktemp "${state_dir}/client.XXXXXX")"
  chmod 0600 "${client_file}"
  cat >"${client_file}" <<EOF
[client]
user=root
socket=/run/mysqld/mysqld.sock
EOF
  emit_progress 88 secure_database "正在设置 MySQL 管理账户"
  MYSQL_TEST_LOGIN_FILE="${login_path_file}" "${install_dir}/bin/mysql" \
    --defaults-file="${client_file}" --user=root --protocol=socket \
    --socket=/run/mysqld/mysqld.sock --skip-password <<EOF
ALTER USER 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
  DROP USER IF EXISTS 'root'@'127.0.0.1';
  CREATE USER 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;
DELETE FROM mysql.user WHERE User='';
FLUSH PRIVILEGES;
EOF
fi
if [[ -n "${mysql_password}" ]]; then
  if [[ "${new_database}" != "true" ]]; then
    client_file="$(mktemp "${state_dir}/client.XXXXXX")"
    chmod 0600 "${client_file}"
  fi
  cat >"${client_file}" <<EOF
[client]
user=root
password='${mysql_password}'
host=127.0.0.1
port=${mysql_port}
protocol=tcp
get-server-public-key
EOF
  emit_progress 94 verify_loopback_login "正在验证 MySQL 回环连接"
  root_login_output=""
  if ! root_login_output="$(MYSQL_TEST_LOGIN_FILE="${login_path_file}" "${install_dir}/bin/mysql" \
    --defaults-file="${client_file}" --user=root --host=127.0.0.1 \
    --port="${mysql_port}" --protocol=tcp \
    --batch --skip-column-names --execute="SELECT 1" 2>&1)" ||
    ! grep -Fxq "1" <<<"${root_login_output}"; then
    if [[ "${new_database}" != true ]] &&
      existing_root_password_reset_authorized &&
      grep -Eqi "access denied|authentication plugin|using password" <<<"${root_login_output}"; then
      recover_existing_root_password
      root_login_output="$(MYSQL_TEST_LOGIN_FILE="${login_path_file}" "${install_dir}/bin/mysql" \
        --defaults-file="${client_file}" --user=root --host=127.0.0.1 \
        --port="${mysql_port}" --protocol=tcp \
        --batch --skip-column-names --execute="SELECT 1" 2>&1)" || {
        [[ -z "${root_login_output}" ]] || printf '%s\n' "${root_login_output}" >&2
        die "MySQL root login failed after password recovery."
      }
      grep -Fxq "1" <<<"${root_login_output}" || die "MySQL root login verification returned an unexpected result after password recovery."
    else
      [[ -z "${root_login_output}" ]] || printf '%s\n' "${root_login_output}" >&2
      die "MySQL root loopback login verification failed."
    fi
  fi
  if [[ "${mysql_username}" != "root" ]]; then
    emit_progress 96 create_database_login "正在创建 MySQL 登录用户"
    MYSQL_TEST_LOGIN_FILE="${login_path_file}" "${install_dir}/bin/mysql" \
      --defaults-file="${client_file}" --user=root --host=127.0.0.1 \
      --port="${mysql_port}" --protocol=tcp <<EOF
    CREATE USER IF NOT EXISTS '${mysql_username}'@'localhost' IDENTIFIED BY '${mysql_password}';
    ALTER USER '${mysql_username}'@'localhost' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_username}'@'localhost' WITH GRANT OPTION;
    CREATE USER IF NOT EXISTS '${mysql_username}'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
    ALTER USER '${mysql_username}'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_username}'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
EOF
  fi
fi
persist_install_parameters
emit_progress 100 configure_completed "MySQL 配置和服务部署完成"
echo "MySQL configuration and service installed."
