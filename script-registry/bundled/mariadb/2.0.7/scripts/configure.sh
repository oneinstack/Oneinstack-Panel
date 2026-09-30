#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
ensure_account
install -d -m 0755 -- "$(dirname -- "${config_file}")"
install -d -o "${run_user}" -g "${run_group}" -m 0750 -- "${data_dir}" "${log_dir}"
plugin_dir="$(runtime_plugin_dir)" || die "MariaDB runtime plugin directory is missing."
share_dir="$(runtime_share_dir)" || die "MariaDB runtime character set and message files are missing."

if command -v ss >/dev/null 2>&1 &&
  ss -H -ltn "sport = :${mysql_port}" 2>/dev/null | grep -q .; then
  current_port="$(sed -nE 's/^[[:space:]]*port[[:space:]]*=[[:space:]]*([0-9]+).*/\1/p' "${config_file}" 2>/dev/null | tail -n1)"
  if [[ "${current_port}" != "${mysql_port}" ]] || ! managed_installation_present || ! service_is_active; then
    die "MariaDB port ${mysql_port} became occupied before configuration was published."
  fi
fi

emit_progress 12 write_config "正在写入 MariaDB 受管配置"
if [[ ! -f "${config_file}" ]]; then
  candidate_config="$(mktemp "$(dirname -- "${config_file}")/.oneinstack-mariadb.XXXXXX")"
  {
    printf '[client]\n'
    printf 'port=%s\n' "${mysql_port}"
    printf 'socket=/run/mariadb/mariadb.sock\n'
    printf 'default-character-set=utf8mb4\n'
    printf 'character-sets-dir=%s/charsets\n' "${share_dir}"
    printf '[mariadbd]\n'
    printf 'user=%s\n' "${run_user}"
    printf 'basedir=%s\n' "${install_dir}"
    printf 'plugin-dir=%s\n' "${plugin_dir}"
    printf 'lc-messages-dir=%s\n' "${share_dir}"
    printf 'character-sets-dir=%s/charsets\n' "${share_dir}"
    printf 'datadir=%s\n' "${data_dir}"
    printf 'port=%s\n' "${mysql_port}"
    printf 'bind-address=%s\n' "${bind_address}"
    printf 'socket=/run/mariadb/mariadb.sock\n'
    printf 'pid-file=/run/mariadb/mariadb.pid\n'
    printf 'log-error=%s/mariadb-error.log\n' "${log_dir}"
    printf 'skip-name-resolve=1\n'
    printf 'character-set-server=utf8mb4\n'
    printf 'collation-server=utf8mb4_unicode_ci\n'
    printf 'default-time-zone=+08:00\n'
    printf 'max_connections=300\n'
    printf 'max_allowed_packet=64M\n'
    printf 'innodb_buffer_pool_size=128M\n'
    printf 'slow_query_log=OFF\n'
    printf 'long_query_time=10\n'
    printf 'open_files_limit=65535\n'
    printf '[mariadb-dump]\nquick\nmax_allowed_packet=64M\n'
  } >"${candidate_config}"
  chmod 0640 "${candidate_config}"
  chown root:"${run_group}" "${candidate_config}"
  "${install_dir}/bin/mariadbd" --defaults-file="${candidate_config}" --help --verbose >/dev/null ||
    die "Generated MariaDB configuration is invalid."
  mv -f -- "${candidate_config}" "${config_file}"
fi

emit_progress 28 write_service "正在写入 MariaDB systemd 服务"
candidate_unit="$(mktemp "$(dirname -- "${unit_file}")/.oneinstack-mariadb-service.XXXXXX")"
cat >"${candidate_unit}" <<EOF
[Unit]
Description=Oneinstack MariaDB Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${run_user}
Group=${run_group}
RuntimeDirectory=mariadb
RuntimeDirectoryMode=0755
ExecStart=${install_dir}/bin/mariadbd --defaults-file=${config_file}
Restart=on-failure
RestartSec=5
TimeoutStartSec=900
LimitNOFILE=65535
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${candidate_unit}"
mv -f -- "${candidate_unit}" "${unit_file}"

new_database=false
bootstrap_begin="# BEGIN ONEINSTACK MARIADB SOCKET-ONLY BOOTSTRAP"
bootstrap_end="# END ONEINSTACK MARIADB SOCKET-ONLY BOOTSTRAP"
if [[ ! -d "${data_dir}/mysql" ]]; then
  [[ -n "${mysql_password}" ]] || die "MYSQL_PASSWORD is required for a new MariaDB database."
  install_db="${install_dir}/scripts/mariadb-install-db"
  [[ -x "${install_db}" ]] || install_db="${install_dir}/bin/mariadb-install-db"
  [[ -x "${install_db}" ]] || die "mariadb-install-db is missing."
  emit_progress 45 initialize_database "正在初始化 MariaDB 数据目录"
  "${install_db}" --defaults-file="${config_file}" --user="${run_user}" \
    --basedir="${install_dir}" --datadir="${data_dir}" \
    --auth-root-authentication-method=normal --skip-test-db
  : >"${state_dir}/initialized-this-run"
  new_database=true
fi

if [[ "${new_database}" == true ]]; then
  bootstrap_config="$(mktemp "$(dirname -- "${config_file}")/.oneinstack-mariadb-bootstrap.XXXXXX")"
  cp -p -- "${config_file}" "${bootstrap_config}"
  {
    printf '\n%s\n' "${bootstrap_begin}"
    printf '[mariadbd]\nskip-networking=1\n'
    printf '%s\n' "${bootstrap_end}"
  } >>"${bootstrap_config}"
  "${install_dir}/bin/mariadbd" --defaults-file="${bootstrap_config}" --help --verbose >/dev/null ||
    die "MariaDB socket-only bootstrap configuration is invalid."
  mv -f -- "${bootstrap_config}" "${config_file}"
fi

normalize_runtime_permissions
emit_progress 62 service_start "正在启动 MariaDB 服务"
service_start
ready=false
for _ in $(seq 1 180); do
  if [[ -S /run/mariadb/mariadb.sock ]]; then
    ready=true
    break
  fi
  service_is_active || die "MariaDB service exited before becoming ready."
  sleep 1
done
[[ "${ready}" == true ]] || die "MariaDB socket was not ready within 180 seconds."

client_file="$(mktemp "${state_dir}/client.XXXXXX")"
chmod 0600 "${client_file}"
cleanup_client() { rm -f -- "${client_file}"; }
trap cleanup_client EXIT

if [[ "${new_database}" == true ]]; then
  {
    printf '[client]\n'
    printf 'user=root\n'
    printf 'socket=/run/mariadb/mariadb.sock\n'
    printf 'protocol=socket\n'
  } >"${client_file}"
  emit_progress 76 secure_database "正在设置 MariaDB 管理账户"
  "${install_dir}/bin/mariadb" --defaults-file="${client_file}" <<EOF
ALTER USER 'root'@'localhost' IDENTIFIED BY '${mysql_password}';
DROP USER IF EXISTS 'root'@'127.0.0.1';
CREATE USER 'root'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO 'root'@'127.0.0.1' WITH GRANT OPTION;
DELETE FROM mysql.user WHERE User='';
DROP DATABASE IF EXISTS test;
FLUSH PRIVILEGES;
EOF
  network_config="$(mktemp "$(dirname -- "${config_file}")/.oneinstack-mariadb-network.XXXXXX")"
  sed "/^${bootstrap_begin}$/,/^${bootstrap_end}$/d" "${config_file}" >"${network_config}"
  chmod --reference="${config_file}" "${network_config}"
  chown --reference="${config_file}" "${network_config}"
  "${install_dir}/bin/mariadbd" --defaults-file="${network_config}" --help --verbose >/dev/null ||
    die "MariaDB post-bootstrap configuration is invalid."
  if command -v ss >/dev/null 2>&1 &&
    ss -H -ltn "sport = :${mysql_port}" 2>/dev/null | grep -q .; then
    die "MariaDB port ${mysql_port} became occupied during socket-only bootstrap."
  fi
  mv -f -- "${network_config}" "${config_file}"
  service_restart
  for _ in $(seq 1 180); do
    [[ -S /run/mariadb/mariadb.sock ]] && service_is_active && break
    sleep 1
  done
  if [[ ! -S /run/mariadb/mariadb.sock ]] || ! service_is_active; then
    die "MariaDB did not restart with networking after secure bootstrap."
  fi
fi

[[ -n "${mysql_password}" ]] || die "MYSQL_PASSWORD is required to validate managed MariaDB access."
authenticated_username="${mysql_username}"
[[ "${new_database}" != true ]] || authenticated_username=root
{
  printf '[client]\n'
  printf 'user=%s\n' "${authenticated_username}"
  printf "password='%s'\n" "${mysql_password}"
  printf 'host=127.0.0.1\n'
  printf 'port=%s\n' "${mysql_port}"
  printf 'protocol=tcp\n'
} >"${client_file}"

if [[ "${new_database}" != true ]]; then
  emit_progress 82 upgrade_system_tables "正在检查 MariaDB 系统表"
  upgrade_output=""
  if ! upgrade_output="$("${install_dir}/bin/mariadb-upgrade" --defaults-file="${client_file}" --force 2>&1)"; then
    printf '%s\n' "${upgrade_output}" >&2
    if grep -Eqi 'access denied|using password|authentication' <<<"${upgrade_output}"; then
      recover_preserved_root_password || exit 1
      {
        printf '[client]\n'
        printf 'user=root\n'
        printf "password='%s'\n" "${mysql_password}"
        printf 'host=127.0.0.1\n'
        printf 'port=%s\n' "${mysql_port}"
        printf 'protocol=tcp\n'
      } >"${client_file}"
      upgrade_output="$("${install_dir}/bin/mariadb-upgrade" --defaults-file="${client_file}" --force 2>&1)" || {
        printf '%s\n' "${upgrade_output}" >&2
        exit 1
      }
    else
      exit 1
    fi
  fi
  [[ -z "${upgrade_output}" ]] || printf '%s\n' "${upgrade_output}"
fi
if [[ "${mysql_username}" != root ]]; then
  emit_progress 88 create_database_login "正在创建 MariaDB 管理登录用户"
  "${install_dir}/bin/mariadb" --defaults-file="${client_file}" <<EOF
CREATE USER IF NOT EXISTS '${mysql_username}'@'localhost' IDENTIFIED BY '${mysql_password}';
ALTER USER '${mysql_username}'@'localhost' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_username}'@'localhost' WITH GRANT OPTION;
CREATE USER IF NOT EXISTS '${mysql_username}'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
ALTER USER '${mysql_username}'@'127.0.0.1' IDENTIFIED BY '${mysql_password}';
GRANT ALL PRIVILEGES ON *.* TO '${mysql_username}'@'127.0.0.1' WITH GRANT OPTION;
FLUSH PRIVILEGES;
EOF
  {
    printf '[client]\n'
    printf 'user=%s\n' "${mysql_username}"
    printf "password='%s'\n" "${mysql_password}"
    printf 'host=127.0.0.1\n'
    printf 'port=%s\n' "${mysql_port}"
    printf 'protocol=tcp\n'
  } >"${client_file}"
fi
"${install_dir}/bin/mariadb" --defaults-file="${client_file}" \
  --batch --skip-column-names --execute='SELECT 1' | grep -Fxq '1'

persist_install_parameters
emit_progress 100 configure_completed "MariaDB 配置和服务部署完成"
printf 'MariaDB configuration and service installed.\n'
