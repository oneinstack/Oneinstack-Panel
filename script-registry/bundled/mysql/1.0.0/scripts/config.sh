#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
begin_marker="# BEGIN ONEINSTACK PANEL RUNTIME"
end_marker="# END ONEINSTACK PANEL RUNTIME"

revision() { sha256sum "${config_file}" | awk '{print $1}'; }
last_number() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+).*/\\1/p" "${config_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
last_size_mb() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([0-9]+)[mM].*/\\1/p" "${config_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mysqld" ]] || die "MySQL configuration is unavailable."
if [[ "${operation}" == "get" ]]; then
  slow_query_log="$(sed -nE 's/^[[:space:]]*slow_query_log[[:space:]]*=[[:space:]]*(ON|OFF|1|0).*/\1/Ip' "${config_file}" | tail -n1)"
  case "${slow_query_log^^}" in ON|1) slow_query_log=true ;; *) slow_query_log=false ;; esac
  printf 'component=mysql\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'maxConnections=%s\nmaxAllowedPacket=%s\ninnodbBufferPoolSize=%s\nslowQueryLog=%s\nlongQueryTime=%s\n' \
    "$(last_number max_connections 300)" "$(last_size_mb max_allowed_packet 64)" \
    "$(last_size_mb innodb_buffer_pool_size 128)" "${slow_query_log}" "$(last_number long_query_time 10)"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}
max_connections="${ONEINSTACK_CONFIG_MAX_CONNECTIONS:-}"
max_allowed_packet="${ONEINSTACK_CONFIG_MAX_ALLOWED_PACKET:-}"
buffer_pool="${ONEINSTACK_CONFIG_INNODB_BUFFER_POOL_SIZE:-}"
slow_query_log="${ONEINSTACK_CONFIG_SLOW_QUERY_LOG:-}"
long_query_time="${ONEINSTACK_CONFIG_LONG_QUERY_TIME:-}"
[[ "${max_connections}" =~ ^[0-9]+$ && "${max_connections}" -ge 10 && "${max_connections}" -le 100000 ]] || die "Invalid maxConnections."
[[ "${max_allowed_packet}" =~ ^[0-9]+$ && "${max_allowed_packet}" -ge 1 && "${max_allowed_packet}" -le 1024 ]] || die "Invalid maxAllowedPacket."
[[ "${buffer_pool}" =~ ^[0-9]+$ && "${buffer_pool}" -ge 128 && "${buffer_pool}" -le 1048576 ]] || die "Invalid innodbBufferPoolSize."
[[ "${slow_query_log}" == "true" || "${slow_query_log}" == "false" ]] || die "Invalid slowQueryLog."
[[ "${long_query_time}" =~ ^[0-9]+$ && "${long_query_time}" -ge 1 && "${long_query_time}" -le 600 ]] || die "Invalid longQueryTime."

emit_progress 8 config_snapshot "正在创建 MySQL 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/my.cnf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-mycnf.XXXXXX")"
sed "/^${begin_marker}$/,/^${end_marker}$/d" "${config_file}" >"${candidate}"
cat >>"${candidate}" <<EOF

${begin_marker}
[mysqld]
max_connections=${max_connections}
max_allowed_packet=${max_allowed_packet}M
innodb_buffer_pool_size=${buffer_pool}M
slow_query_log=$([[ "${slow_query_log}" == "true" ]] && printf ON || printf OFF)
long_query_time=${long_query_time}
${end_marker}
EOF
chmod --reference="${config_file}" "${candidate}"
chown --reference="${config_file}" "${candidate}"

emit_progress 35 config_validate "正在校验 MySQL 候选配置"
if ! "${install_dir}/bin/mysqld" --defaults-file="${candidate}" --validate-config --user=mysql; then
  rm -f -- "${candidate}"
  printf 'MySQL candidate configuration is invalid.\n' >&2
  exit 65
fi
was_active=false
systemctl is-active --quiet mysql.service && was_active=true
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-mycnf-restore.XXXXXX")"
    cp -p -- "${backup_dir}/my.cnf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    [[ "${was_active}" == "true" ]] && systemctl restart mysql.service
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

emit_progress 62 config_publish "正在原子发布 MySQL 配置"
mv -f -- "${candidate}" "${config_file}"
committed=true
if [[ "${was_active}" == "true" ]]; then
  emit_progress 80 config_restart "正在重启 MySQL"
  systemctl restart mysql.service
  systemctl is-active --quiet mysql.service
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "MySQL 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
