#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_base_commands
check_host
ensure_account

config_file="${install_dir}/etc/redis.conf"
previous_config="${rollback_dir}/install/etc/redis.conf"
if [[ ! -f "${previous_config}" && -f "${config_file}" ]]; then
  previous_config="${config_file}"
fi
if [[ ! -f "${previous_config}" && -f "${external_migration_dir}/config/redis.conf" ]]; then
  previous_config="${external_migration_dir}/config/redis.conf"
fi

config_value() {
  local file="$1" key="$2" fallback="$3" value
  value="$(awk -v key="${key}" '$1 == key { $1=""; sub(/^[[:space:]]+/, ""); print; found=1 } END { if (!found) exit 1 }' "${file}" 2>/dev/null || true)"
  printf '%s' "${value:-${fallback}}"
}

rewrite_directive() {
  local file="$1" key="$2" value="$3" temporary
  temporary="$(mktemp "$(dirname -- "${file}")/.oneinstack-redis-config.XXXXXX")"
  awk -v key="${key}" -v value="${value}" '
    BEGIN { replaced=0 }
    $1 == key {
      if (!replaced) { print key " " value; replaced=1 }
      next
    }
    { print }
    END { if (!replaced) print key " " value }
  ' "${file}" >"${temporary}"
  chmod --reference="${file}" "${temporary}"
  chown --reference="${file}" "${temporary}"
  mv -f -- "${temporary}" "${file}"
}

config_acl_username() {
  local file="$1"
  awk '$1 == "user" && $2 != "default" && $2 != "" { print $2; found=1; exit } END { if (!found) print "default" }' "${file}"
}

config_acl_password() {
  local file="$1" username="$2"
  awk -v username="${username}" '
    $1 == "user" && $2 == username {
      for (i = 3; i <= NF; i++) {
        if (substr($i, 1, 1) == ">") {
          print substr($i, 2)
          exit
        }
      }
    }
  ' "${file}"
}

rewrite_authentication() {
  local file="$1" username="$2" password="$3" temporary
  temporary="$(mktemp "$(dirname -- "${file}")/.oneinstack-redis-auth.XXXXXX")"
  awk -v begin="# BEGIN ONEINSTACK PANEL AUTH" -v end="# END ONEINSTACK PANEL AUTH" \
    -v username="${username}" '
    $0 == begin { managed=1; next }
    $0 == end { managed=0; next }
    managed { next }
    $1 == "requirepass" { next }
    $1 == "user" && ($2 == "default" || $2 == username) { next }
    { print }
  ' "${file}" >"${temporary}"
  {
    printf '\n# BEGIN ONEINSTACK PANEL AUTH\n'
    if [[ "${username}" != "default" ]]; then
      printf 'user default off\n'
    fi
    if [[ -n "${password}" ]]; then
      printf 'user %s on >%s ~* &* +@all\n' "${username}" "${password}"
    else
      printf 'user %s on nopass ~* &* +@all\n' "${username}"
    fi
    printf '# END ONEINSTACK PANEL AUTH\n'
  } >>"${temporary}"
  chmod --reference="${file}" "${temporary}"
  chown --reference="${file}" "${temporary}"
  mv -f -- "${temporary}" "${file}"
}

write_initial_config() {
  install -d -o root -g redis -m 0750 -- "${install_dir}/etc"
  cat >"${config_file}" <<EOF
bind ${redis_bind}
protected-mode yes
port ${redis_port}
tcp-backlog 511
timeout 0
tcp-keepalive 300
daemonize no
supervised no
loglevel notice
logfile ""
databases 16
dir ${data_dir}
dbfilename dump.rdb
appendonly yes
appendfilename "appendonly.aof"
appenddirname "appendonlydir"
appendfsync everysec
save 3600 1
save 300 100
save 60 10000
EOF
  rewrite_authentication "${config_file}" "${redis_username}" "${redis_password}"
}

preserve_previous_config() {
  install -d -o root -g redis -m 0750 -- "${install_dir}/etc"
  cp -a -- "${previous_config}" "${config_file}"
  if ! parameter_explicit DATA_DIR; then
    data_dir="$(config_value "${config_file}" dir "${data_dir}")"
    validate_path "${data_dir}" DATA_DIR
  else
    rewrite_directive "${config_file}" dir "${data_dir}"
  fi
  if parameter_explicit REDIS_BIND; then
    rewrite_directive "${config_file}" bind "${redis_bind}"
  else
    redis_bind="$(config_value "${config_file}" bind "${redis_bind}")"
  fi
  if parameter_explicit REDIS_PORT; then
    rewrite_directive "${config_file}" port "${redis_port}"
  else
    redis_port="$(config_value "${config_file}" port "${redis_port}")"
  fi
  if ! parameter_explicit REDIS_USERNAME; then
    redis_username="$(config_acl_username "${config_file}")"
  fi
  if ! parameter_explicit REDIS_PASSWORD; then
    redis_password="$(config_acl_password "${config_file}" "${redis_username}")"
    if [[ -z "${redis_password}" ]]; then
      redis_password="$(config_value "${config_file}" requirepass "${redis_password}")"
    fi
  fi
  rewrite_authentication "${config_file}" "${redis_username}" "${redis_password}"
}

emit_progress 10 prepare_directories "正在创建 Redis 数据和配置目录"
install -d -o redis -g redis -m 0750 -- "${data_dir}"
ensure_data_path_access
migrate_external_redis_data
if [[ -f "${previous_config}" ]]; then
  emit_progress 30 preserve_config "正在保留已有 Redis 配置"
  preserve_previous_config
else
  emit_progress 30 write_config "正在写入 Redis 配置"
  write_initial_config
fi
validate_bind
[[ "${redis_username}" =~ ^[A-Za-z0-9._-]{1,64}$ ]] || die "Invalid REDIS_USERNAME."
if [[ "${redis_username}" != "default" && -z "${redis_password}" ]]; then
  die "REDIS_PASSWORD is required when REDIS_USERNAME is not default."
fi
[[ "${redis_port}" =~ ^[0-9]+$ && "${redis_port}" -ge 1 && "${redis_port}" -le 65535 ]] || die "Invalid REDIS_PORT."
chown root:redis "${config_file}"
chmod 0640 "${config_file}"
normalize_runtime_permissions
verify_runtime_permissions

emit_progress 65 write_service "正在写入 Redis systemd 服务"
install -d -m 0755 -- "$(dirname -- "${unit_file}")"
cat >"${unit_file}" <<EOF
[Unit]
Description=Redis persistent key-value database
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=redis
Group=redis
ExecStart=${install_dir}/bin/redis-server ${config_file}
ExecStop=/bin/kill -s TERM \$MAINPID
Restart=on-failure
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ReadWritePaths=${data_dir}

[Install]
WantedBy=multi-user.target
EOF
chmod 0644 "${unit_file}"

emit_progress 80 config_validate "正在执行 Redis 原生配置校验"
validate_redis_config "${config_file}" || die "Redis configuration is invalid."
emit_progress 90 service_start "正在启动 Redis 服务"
systemctl daemon-reload
systemctl enable "${service_name}" >/dev/null
systemctl restart "${service_name}"
if ! wait_for_redis_ready 180 0.5; then
  redis_start_failure_diagnostics
  die "Redis did not become ready."
fi
emit_progress 100 configure_completed "Redis 配置和服务部署完成"
echo "Redis configuration and systemd service installed."
