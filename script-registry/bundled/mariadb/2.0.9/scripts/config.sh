#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
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
last_string() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}[[:space:]]*=[[:space:]]*([^[:space:]#]+).*/\\1/p" "${config_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
unit_string() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}=[[:space:]]*([^[:space:]#]+).*/\\1/p" "${unit_file}" 2>/dev/null | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
sed_escape_replacement() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}
wait_for_port() {
  local port="$1"
  for _ in $(seq 1 60); do
    if ss -H -ltn "sport = :${port}" 2>/dev/null | grep -Eq "[:.]${port}[[:space:]]"; then
      return 0
    fi
    sleep 1
  done
  die "MariaDB did not listen on port ${port} after restart."
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mariadbd" ]] || die "MariaDB configuration is unavailable."
if [[ "${operation}" == "get" ]]; then
  slow_query_log="$(sed -nE 's/^[[:space:]]*slow_query_log[[:space:]]*=[[:space:]]*(ON|OFF|1|0).*/\1/Ip' "${config_file}" | tail -n1)"
  case "${slow_query_log^^}" in ON|1) slow_query_log=true ;; *) slow_query_log=false ;; esac
  effective_port="$(last_number port "${mysql_port}")"
  effective_bind_address="$(last_string bind-address "${bind_address}")"
  effective_install_dir="$(last_string basedir "${install_dir}")"
  effective_data_dir="$(last_string datadir "${data_dir}")"
  effective_log_file="$(last_string log-error "${log_dir}/mariadb-error.log")"
  effective_log_dir="$(dirname -- "${effective_log_file}")"
  effective_run_user="$(last_string user "${run_user}")"
  effective_run_group="${run_group}"
  if [[ -f "${unit_file}" ]]; then
    effective_run_user="$(unit_string User "${effective_run_user}")"
    effective_run_group="$(unit_string Group "${effective_run_group}")"
  fi
  runtime_version="$("${effective_install_dir}/bin/mariadbd" --version 2>&1 |
    grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true)"
  printf 'component=mariadb\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'mariadbPort=%s\nbindAddress=%s\n' "${effective_port}" "${effective_bind_address}"
  printf 'runtime.port=%s\nruntime.bindAddress=%s\nruntime.socketPath=/run/mariadb/mariadb.sock\nruntime.installDir=%s\nruntime.dataDir=%s\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\nruntime.configFile=%s\nruntime.serviceName=mariadb\nruntime.version=%s\n' \
    "${effective_port}" "${effective_bind_address}" "${effective_install_dir}" "${effective_data_dir}" "${effective_log_dir}" "${effective_run_user}" "${effective_run_group}" \
    "${config_file}" "${runtime_version}"
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
target_mysql_port="${ONEINSTACK_CONFIG_MARIADB_PORT:-${mysql_port}}"
target_bind_address="${ONEINSTACK_CONFIG_BIND_ADDRESS:-${bind_address}}"
target_install_dir="${ONEINSTACK_CONFIG_INSTALL_DIR:-${install_dir}}"
target_data_dir="${ONEINSTACK_CONFIG_DATA_DIR:-${data_dir}}"
target_log_dir="${ONEINSTACK_CONFIG_LOG_DIR:-${log_dir}}"
target_run_user="${ONEINSTACK_CONFIG_RUN_USER:-${run_user}}"
target_run_group="${ONEINSTACK_CONFIG_RUN_GROUP:-${run_group}}"
[[ "${max_connections}" =~ ^[0-9]+$ && "${max_connections}" -ge 10 && "${max_connections}" -le 100000 ]] || die "Invalid maxConnections."
[[ "${max_allowed_packet}" =~ ^[0-9]+$ && "${max_allowed_packet}" -ge 1 && "${max_allowed_packet}" -le 1024 ]] || die "Invalid maxAllowedPacket."
[[ "${buffer_pool}" =~ ^[0-9]+$ && "${buffer_pool}" -ge 128 && "${buffer_pool}" -le 1048576 ]] || die "Invalid innodbBufferPoolSize."
[[ "${slow_query_log}" == "true" || "${slow_query_log}" == "false" ]] || die "Invalid slowQueryLog."
[[ "${long_query_time}" =~ ^[0-9]+$ && "${long_query_time}" -ge 1 && "${long_query_time}" -le 600 ]] || die "Invalid longQueryTime."
[[ "${target_mysql_port}" =~ ^[0-9]+$ && "${target_mysql_port}" -ge 1 && "${target_mysql_port}" -le 65535 ]] || die "Invalid mariadbPort."
validate_ip_address "${target_bind_address}" || die "bindAddress must be an IP address."
validate_path "${target_install_dir}" INSTALL_DIR
validate_path "${target_data_dir}" DATA_DIR
validate_path "${target_log_dir}" LOG_DIR
[[ "${target_install_dir}" == "${install_dir}" ]] || die "INSTALL_DIR cannot be changed after MariaDB installation; reinstall with the new directory."
[[ "${target_data_dir}" == "${data_dir}" ]] || die "DATA_DIR cannot be changed by the configuration action; use a data migration flow."
[[ "${target_run_user}" == "${run_user}" ]] || die "RUN_USER cannot be changed by the configuration action; reinstall with the new runtime account."
[[ "${target_run_group}" == "${run_group}" ]] || die "RUN_GROUP cannot be changed by the configuration action; reinstall with the new runtime group."
case "${target_install_dir}${target_data_dir}${target_log_dir}" in
  *[[:space:]]*) die "MariaDB configuration paths cannot contain spaces." ;;
esac
current_port="$(last_number port "${mysql_port}")"
if [[ "${target_mysql_port}" != "${current_port}" ]] && command -v ss >/dev/null 2>&1 &&
  ss -H -ltn "sport = :${target_mysql_port}" 2>/dev/null | grep -q .; then
  die "MariaDB target port ${target_mysql_port} is already occupied."
fi

escaped_install_dir="$(sed_escape_replacement "${target_install_dir}")"
escaped_data_dir="$(sed_escape_replacement "${target_data_dir}")"
escaped_log_dir="$(sed_escape_replacement "${target_log_dir}")"
install -d -m 0750 -- "${target_log_dir}"
chown "${run_user}:${run_group}" "${target_log_dir}"

emit_progress 8 config_snapshot "正在创建 MariaDB 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/my.cnf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-mycnf.XXXXXX")"
sed "/^${begin_marker}$/,/^${end_marker}$/d" "${config_file}" >"${candidate}"
sed -Ei \
  -e "s|^[[:space:]]*port[[:space:]]*=.*|port=${target_mysql_port}|" \
  -e "s|^[[:space:]]*bind-address[[:space:]]*=.*|bind-address=${target_bind_address}|" \
  -e "s|^[[:space:]]*basedir[[:space:]]*=.*|basedir=${escaped_install_dir}|" \
  -e "s|^[[:space:]]*datadir[[:space:]]*=.*|datadir=${escaped_data_dir}|" \
  -e "s|^[[:space:]]*log-error[[:space:]]*=.*|log-error=${escaped_log_dir}/mariadb-error.log|" \
  -e "s|^[[:space:]]*user[[:space:]]*=.*|user=${run_user}|" \
  "${candidate}"
cat >>"${candidate}" <<EOF

${begin_marker}
[mariadbd]
max_connections=${max_connections}
max_allowed_packet=${max_allowed_packet}M
innodb_buffer_pool_size=${buffer_pool}M
slow_query_log=$([[ "${slow_query_log}" == "true" ]] && printf ON || printf OFF)
long_query_time=${long_query_time}
${end_marker}
EOF
chmod --reference="${config_file}" "${candidate}"
chown --reference="${config_file}" "${candidate}"

emit_progress 35 config_validate "正在校验 MariaDB 候选配置"
if ! "${install_dir}/bin/mariadbd" --defaults-file="${candidate}" --help --verbose >/dev/null; then
  rm -f -- "${candidate}"
  printf 'MariaDB candidate configuration is invalid.\n' >&2
  exit 65
fi
was_active=false
service_is_active && was_active=true
committed=false
completed=false
health_client=""
if [[ "${was_active}" == true ]]; then
  [[ -n "${mysql_password}" ]] || die "Managed MariaDB credential is required to validate a running service."
  health_client="$(mktemp "${state_dir}/config-client.XXXXXX")"
  chmod 0600 "${health_client}"
  {
    printf '[client]\n'
    printf 'user=%s\n' "${mysql_username}"
    printf "password='%s'\n" "${mysql_password}"
    printf 'socket=/run/mariadb/mariadb.sock\n'
    printf 'protocol=socket\n'
  } >"${health_client}"
fi
rollback() {
  local code="${1:-$?}"
  local restored=false
  trap - ERR INT TERM EXIT
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-mycnf-restore.XXXXXX")"
    cp -p -- "${backup_dir}/my.cnf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    if [[ "${was_active}" == "true" ]]; then
      service_restart
      for _ in $(seq 1 60); do
        if service_is_active && [[ -S /run/mariadb/mariadb.sock ]]; then
          restored_output="$("${install_dir}/bin/mariadb" --defaults-file="${health_client}" --batch --skip-column-names --execute='SELECT 1' 2>/dev/null)"
          if grep -Fxq '1' <<<"${restored_output}"; then
            restored=true
            break
          fi
        fi
        sleep 1
      done
      if [[ "${restored}" != true ]]; then
        printf 'MariaDB previous configuration was restored, but the old service health check failed.\n' >&2
        code=70
      fi
    fi
  fi
  rm -f -- "${candidate:-}" "${health_client:-}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM
trap 'code=$?; [[ "${completed}" == true ]] || rollback "${code}"' EXIT

emit_progress 62 config_publish "正在原子发布 MariaDB 配置"
mv -f -- "${candidate}" "${config_file}"
committed=true
if [[ "${was_active}" == "true" ]]; then
  emit_progress 80 config_restart "正在重启 MariaDB"
  mysql_port="${target_mysql_port}"
  bind_address="${target_bind_address}"
  log_dir="${target_log_dir}"
  service_restart
  service_is_active
  wait_for_port "${target_mysql_port}"
  "${install_dir}/bin/mariadb" --defaults-file="${health_client}" \
    --batch --skip-column-names --execute='SELECT 1' | grep -Fxq '1' ||
    die "MariaDB authenticated SQL health check failed after configuration restart."
fi
mysql_port="${target_mysql_port}"
bind_address="${target_bind_address}"
log_dir="${target_log_dir}"
persist_install_parameters
completed=true
trap - ERR INT TERM EXIT
rm -f -- "${health_client:-}"
prune_backups
emit_progress 100 config_applied "MariaDB 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
