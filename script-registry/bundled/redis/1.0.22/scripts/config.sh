#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

config_file="${install_dir}/etc/redis.conf"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
begin_marker="# BEGIN ONEINSTACK PANEL RUNTIME"
end_marker="# END ONEINSTACK PANEL RUNTIME"

revision() { sha256sum "${config_file}" | awk '{print $1}'; }
last_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]+([^#[:space:]]+).*/\\1/p" "${config_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/redis-server" ]] || die "Redis configuration is unavailable."
if [[ "${operation}" == "get" ]]; then
  appendonly="$(last_value appendonly yes)"
  [[ "${appendonly}" == "yes" ]] && appendonly=true || appendonly=false
  maxmemory="$(last_value maxmemory 0)"
  if [[ "${maxmemory}" =~ ^([0-9]+)[mM][bB]?$ ]]; then maxmemory="${BASH_REMATCH[1]}"; elif [[ "${maxmemory}" != "0" ]]; then maxmemory=0; fi
  connection_port="$(last_value port "${redis_port}")"
  connection_bind="$(runtime_config_value bind 2>/dev/null || printf '%s' "${redis_bind}")"
  connection_username="$(runtime_config_username 2>/dev/null || printf 'default')"
  connection_password_configured=false
  connection_requirepass="$(last_value requirepass "" 2>/dev/null)"
  if [[ -n "${connection_requirepass}" && "${connection_requirepass}" != '""' ]] ||
    [[ -n "$(runtime_config_acl_password "${connection_username}" 2>/dev/null || true)" ]]; then
    connection_password_configured=true
  fi
  printf 'component=redis\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'port=%s\nmaxmemory=%s\nmaxmemoryPolicy=%s\nappendonly=%s\ntimeout=%s\ntcpKeepalive=%s\n' \
    "${connection_port}" "${maxmemory}" "$(last_value maxmemory-policy noeviction)" "${appendonly}" \
    "$(last_value timeout 0)" "$(last_value tcp-keepalive 300)"
  printf 'connection.port=%s\nconnection.bindAddress=%s\nconnection.username=%s\nconnection.passwordConfigured=%s\n' \
    "${connection_port}" "${connection_bind}" "${connection_username}" "${connection_password_configured}"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}
verify_runtime_permissions
maxmemory="${ONEINSTACK_CONFIG_MAXMEMORY:-}"
port="${ONEINSTACK_CONFIG_PORT:-}"
policy="${ONEINSTACK_CONFIG_MAXMEMORY_POLICY:-}"
appendonly="${ONEINSTACK_CONFIG_APPENDONLY:-}"
timeout="${ONEINSTACK_CONFIG_TIMEOUT:-}"
tcp_keepalive="${ONEINSTACK_CONFIG_TCP_KEEPALIVE:-}"
[[ "${maxmemory}" =~ ^[0-9]+$ && "${maxmemory}" -le 1048576 ]] || die "Invalid maxmemory."
[[ "${port}" =~ ^[0-9]+$ && "${port}" -ge 1 && "${port}" -le 65535 ]] || die "Invalid port."
case "${policy}" in noeviction|allkeys-lru|allkeys-lfu|allkeys-random|volatile-lru|volatile-lfu|volatile-random|volatile-ttl) ;; *) die "Invalid maxmemoryPolicy." ;; esac
[[ "${appendonly}" == "true" || "${appendonly}" == "false" ]] || die "Invalid appendonly."
[[ "${timeout}" =~ ^[0-9]+$ && "${timeout}" -le 86400 ]] || die "Invalid timeout."
[[ "${tcp_keepalive}" =~ ^[0-9]+$ && "${tcp_keepalive}" -le 3600 ]] || die "Invalid tcpKeepalive."

emit_progress 8 config_snapshot "正在创建 Redis 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/redis.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-redis.XXXXXX")"
sed "/^${begin_marker}$/,/^${end_marker}$/d" "${config_file}" >"${candidate}"
cat >>"${candidate}" <<EOF

${begin_marker}
port ${port}
maxmemory $([[ "${maxmemory}" == "0" ]] && printf 0 || printf '%smb' "${maxmemory}")
maxmemory-policy ${policy}
appendonly $([[ "${appendonly}" == "true" ]] && printf yes || printf no)
timeout ${timeout}
tcp-keepalive ${tcp_keepalive}
${end_marker}
EOF
chmod --reference="${config_file}" "${candidate}"; chown --reference="${config_file}" "${candidate}"

emit_progress 35 config_validate "正在校验 Redis 候选配置"
if ! validate_redis_config "${candidate}"; then
  rm -f -- "${candidate}"
  printf 'Redis candidate configuration is invalid.\n' >&2
  exit 65
fi
was_active=false
systemctl is-active --quiet "${service_name}" && was_active=true
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-redis-restore.XXXXXX")"
    cp -p -- "${backup_dir}/redis.conf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    [[ "${was_active}" == "true" ]] && systemctl restart "${service_name}"
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

emit_progress 62 config_publish "正在原子发布 Redis 配置"
mv -f -- "${candidate}" "${config_file}"
committed=true
if [[ "${was_active}" == "true" ]]; then
  emit_progress 80 config_restart "正在重启 Redis"
  load_runtime_probe_settings || die "Redis runtime probe settings are invalid."
  export REDISCLI_AUTH="${redis_password}"
  systemctl restart "${service_name}"
  if ! wait_for_redis_ready 180 0.5; then
    redis_start_failure_diagnostics
    die "Redis configuration was applied but the runtime PING probe failed."
  fi
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "Redis 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
