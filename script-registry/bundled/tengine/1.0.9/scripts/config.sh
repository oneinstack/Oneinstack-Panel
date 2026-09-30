#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

config_file="$(tengine_main_config || true)"
site_config_file="${install_dir}/conf/conf.d/default.conf"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"

revision() { sha256sum "${config_file}" | awk '{print $1}'; }
read_value() {
  local expression="$1" fallback="$2" value
  value="$(sed -nE "${expression}" "${config_file}" | head -n1)"
  printf '%s' "${value:-${fallback}}"
}
sed_escape_replacement() {
  printf '%s' "$1" | sed 's/[\\&|]/\\&/g'
}
normalize_configured_web_root() {
  local value="${1%/}"
  if [[ "${value}" =~ ^/data/wwwroot(/default)+$ ]]; then
    printf '/data/wwwroot'
  else
    printf '%s' "${value}"
  fi
}
normalize_detected_web_root() {
  local value="${1%/}"
  if [[ "${value}" =~ ^/data/wwwroot(/default)+$ ]]; then
    printf '/data/wwwroot'
  elif [[ "${value}" == */default ]]; then
    printf '%s' "${value%/default}"
  else
    printf '%s' "${value}"
  fi
}
prune_backups() {
  local -a backups=()
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  local index
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

web_root="$(normalize_configured_web_root "${web_root}")"
validate_inputs
runtime_binary="$(tengine_runtime_binary || true)"
[[ -f "${config_file}" && -n "${runtime_binary}" ]] || die "Tengine configuration is unavailable."

if [[ "${operation}" == "get" ]]; then
  worker_processes="$(read_value 's/^[[:space:]]*worker_processes[[:space:]]+([^;]+);/\1/p' auto)"
  worker_connections="$(grep -Eo 'worker_connections[[:space:]]+[0-9]+' "${config_file}" | head -n1 | grep -Eo '[0-9]+' || true)"
  keepalive_timeout="$(read_value 's/^[[:space:]]*keepalive_timeout[[:space:]]+([0-9]+);/\1/p' 65)"
  client_max_body_size="$(read_value 's/^[[:space:]]*client_max_body_size[[:space:]]+([0-9]+)[mM];/\1/p' 1)"
  if [[ -f "${site_config_file}" ]]; then
    detected_port="$(sed -nE 's/^[[:space:]]*listen[[:space:]]+([0-9]+)[[:space:]]+default_server.*;/\1/p' "${site_config_file}" | head -n1)"
    [[ -z "${detected_port}" ]] || tengine_port="${detected_port}"
    detected_web_root="$(sed -nE 's|^[[:space:]]*root[[:space:]]+([^;]+);|\1|p' "${site_config_file}" | head -n1)"
    if [[ -n "${detected_web_root}" && ! -r "${install_parameters_file}" ]]; then
      web_root="$(normalize_detected_web_root "${detected_web_root}")"
    fi
  fi
  detected_identity="$(sed -nE 's/^[[:space:]]*user[[:space:]]+([^;[:space:]]+)([[:space:]]+([^;[:space:]]+))?;.*/\1\t\3/p' "${config_file}" | head -n1)"
  if [[ -n "${detected_identity}" ]]; then
    IFS=$'\t' read -r detected_user detected_group <<<"${detected_identity}"
    run_user="${detected_user}"
    [[ -z "${detected_group}" ]] || run_group="${detected_group}"
  fi
  detected_log_dir="$(sed -nE 's|^[[:space:]]*error_log[[:space:]]+(.+)/tengine-error\.log([[:space:]].*)?;[[:space:]]*$|\1|p' "${config_file}" | head -n1)"
  [[ -z "${detected_log_dir}" ]] || log_dir="${detected_log_dir}"
  printf 'component=tengine\nrevision=%s\napply_mode=reload\n' "$(revision)"
  printf 'workerProcesses=%s\nworkerConnections=%s\nkeepaliveTimeout=%s\nclientMaxBodySize=%s\n' \
    "${worker_processes}" "${worker_connections:-4096}" "${keepalive_timeout}" "${client_max_body_size}"
  printf 'tenginePort=%s\nphpFpmSocket=%s\ninstallDir=%s\nwebRoot=%s\nlogDir=%s\nrunUser=%s\nrunGroup=%s\n' \
    "${tengine_port}" "${php_fpm_socket}" "${install_dir}" "${web_root}" "${log_dir}" "${run_user}" "${run_group}"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die "Unsupported configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}

worker_processes="${ONEINSTACK_CONFIG_WORKER_PROCESSES:-}"
worker_connections="${ONEINSTACK_CONFIG_WORKER_CONNECTIONS:-}"
keepalive_timeout="${ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT:-}"
client_max_body_size="${ONEINSTACK_CONFIG_CLIENT_MAX_BODY_SIZE:-}"
target_tengine_port="${ONEINSTACK_CONFIG_TENGINE_PORT:-${tengine_port}}"
target_php_fpm_socket="${ONEINSTACK_CONFIG_PHP_FPM_SOCKET:-${php_fpm_socket}}"
target_install_dir="${ONEINSTACK_CONFIG_INSTALL_DIR:-${install_dir}}"
target_web_root="$(normalize_configured_web_root "${ONEINSTACK_CONFIG_WEB_ROOT:-${web_root}}")"
target_log_dir="${ONEINSTACK_CONFIG_LOG_DIR:-${log_dir}}"
target_run_user="${ONEINSTACK_CONFIG_RUN_USER:-${run_user}}"
target_run_group="${ONEINSTACK_CONFIG_RUN_GROUP:-${run_group}}"
[[ "${worker_processes}" == "auto" || "${worker_processes}" =~ ^[1-9][0-9]?$ ]] || die "Invalid workerProcesses."
[[ "${worker_connections}" =~ ^[0-9]+$ && "${worker_connections}" -ge 512 && "${worker_connections}" -le 65535 ]] || die "Invalid workerConnections."
[[ "${keepalive_timeout}" =~ ^[0-9]+$ && "${keepalive_timeout}" -ge 5 && "${keepalive_timeout}" -le 300 ]] || die "Invalid keepaliveTimeout."
[[ "${client_max_body_size}" =~ ^[0-9]+$ && "${client_max_body_size}" -ge 1 && "${client_max_body_size}" -le 10240 ]] || die "Invalid clientMaxBodySize."
[[ "${target_tengine_port}" =~ ^[0-9]+$ && "${target_tengine_port}" -ge 1 && "${target_tengine_port}" -le 65535 ]] || die "Invalid tenginePort."
validate_path "${target_php_fpm_socket}" PHP_FPM_SOCKET
[[ "${target_install_dir}" == "${install_dir}" ]] || die "INSTALL_DIR cannot be changed after Tengine installation; reinstall with the new directory."
validate_path "${target_web_root}" WEB_ROOT
validate_path "${target_log_dir}" LOG_DIR
validate_identifier "${target_run_user}"
validate_identifier "${target_run_group}"
case "${target_web_root}${target_log_dir}" in
  *\\*) die "Configuration paths cannot contain backslashes." ;;
esac
escaped_php_fpm_socket="$(sed_escape_replacement "${target_php_fpm_socket}")"
escaped_web_root="$(sed_escape_replacement "${target_web_root}")"
escaped_log_dir="$(sed_escape_replacement "${target_log_dir}")"
getent group "${target_run_group}" >/dev/null || groupadd --system "${target_run_group}"
id "${target_run_user}" >/dev/null 2>&1 ||
  useradd --system --gid "${target_run_group}" --home-dir /nonexistent --shell /usr/sbin/nologin "${target_run_user}"
emit_progress 8 config_snapshot "正在创建 Tengine 配置快照"
install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/tengine.conf"
[[ ! -f "${site_config_file}" ]] || cp -a -- "${site_config_file}" "${backup_dir}/default.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
path_acl_transaction_file="${backup_dir}/path-acl-added"
: >"${path_acl_transaction_file}"
chmod 0600 "${path_acl_transaction_file}"

install -d -m 0755 -- "${target_web_root}" "${target_web_root}/default"
install -d -m 0750 -- "${target_log_dir}"
chown "${target_run_user}:${target_run_group}" "${target_web_root}" "${target_web_root}/default" "${target_log_dir}"

candidate="$(mktemp "$(dirname "${config_file}")/.oneinstack-tengine.XXXXXX")"
cp -p -- "${config_file}" "${candidate}"
sed -Ei \
  -e "s/^[[:space:]]*worker_processes[[:space:]]+[^;]+;/worker_processes ${worker_processes};/" \
  -e "s/worker_connections[[:space:]]+[0-9]+;/worker_connections ${worker_connections};/" \
  -e "s/^[[:space:]]*keepalive_timeout[[:space:]]+[0-9]+;/    keepalive_timeout ${keepalive_timeout};/" \
  -e "s|^[[:space:]]*user[[:space:]]+[^;]+;|user ${target_run_user} ${target_run_group};|" \
  -e "s|^[[:space:]]*error_log[[:space:]]+[^;]+;|error_log ${escaped_log_dir}/tengine-error.log warn;|" \
  -e "s|^[[:space:]]*access_log[[:space:]]+[^;]+;|    access_log ${escaped_log_dir}/tengine-access.log main;|" \
  "${candidate}"
if grep -Eq '^[[:space:]]*client_max_body_size[[:space:]]+' "${candidate}"; then
  sed -Ei "s/^[[:space:]]*client_max_body_size[[:space:]]+[^;]+;/    client_max_body_size ${client_max_body_size}m;/" "${candidate}"
else
  sed -i "/^[[:space:]]*server_tokens[[:space:]]/i\\    client_max_body_size ${client_max_body_size}m;" "${candidate}"
fi
site_candidate=""
if [[ -f "${site_config_file}" ]]; then
  site_candidate="$(mktemp "$(dirname "${site_config_file}")/.oneinstack-tengine-site.XXXXXX")"
  cp -p -- "${site_config_file}" "${site_candidate}"
  sed -Ei \
    -e "s|^([[:space:]]*listen[[:space:]]+)[0-9]+([[:space:]]+default_server.*;)|\\1${target_tengine_port}\\2|" \
    -e "s|^[[:space:]]*root[[:space:]]+[^;]+;|    root ${escaped_web_root}/default;|" \
	    -e "s|^[[:space:]]*fastcgi_pass[[:space:]]+unix:[^;]+;|        fastcgi_pass unix:${escaped_php_fpm_socket};|" \
    "${site_candidate}"
fi

emit_progress 35 config_validate "正在校验 Tengine 候选配置"
if ! "${runtime_binary}" -t -p "${install_dir}/" -c "${candidate}"; then
  rm -f -- "${candidate}" "${site_candidate:-}"
  printf 'Tengine candidate configuration is invalid.\n' >&2
  exit 65
fi
reload_configured_service() {
  systemd_available || return 0
  if systemctl is-active --quiet "${service_name}.service"; then
    systemctl reload "${service_name}.service"
  elif legacy_tengine_service_matches; then
    systemctl reload "${legacy_service_name}.service"
  fi
}
was_active=false
if systemd_available && (systemctl is-active --quiet "${service_name}.service" || legacy_tengine_is_active); then
  was_active=true
fi
committed=false
rollback() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == "true" ]]; then
    restore="$(mktemp "$(dirname "${config_file}")/.oneinstack-tengine-restore.XXXXXX")"
    cp -p -- "${backup_dir}/tengine.conf" "${restore}"
    mv -f -- "${restore}" "${config_file}"
    if [[ -f "${backup_dir}/default.conf" ]]; then
      restore_site="$(mktemp "$(dirname "${site_config_file}")/.oneinstack-tengine-site-restore.XXXXXX")"
      cp -p -- "${backup_dir}/default.conf" "${restore_site}"
      mv -f -- "${restore_site}" "${site_config_file}"
    fi
    [[ "${was_active}" == "true" ]] && reload_configured_service
  fi
  rm -f -- "${candidate:-}" "${site_candidate:-}"
  restore_transaction_path_acl "${path_acl_transaction_file}"
  exit "${code}"
}
trap 'rollback $?' ERR
trap 'rollback 130' INT
trap 'rollback 143' TERM

ensure_runtime_path_traversal "${target_run_user}" "${target_web_root}" "WEB_ROOT"
ensure_runtime_path_traversal "${target_run_user}" "${target_log_dir}" "LOG_DIR"

emit_progress 62 config_publish "正在原子发布 Tengine 配置"
mv -f -- "${candidate}" "${config_file}"
[[ -z "${site_candidate}" ]] || mv -f -- "${site_candidate}" "${site_config_file}"
committed=true
if [[ "${was_active}" == "true" ]]; then
  emit_progress 82 config_reload "正在平滑重载 Tengine"
  reload_configured_service
  tengine_is_running || die "Tengine did not remain active after configuration reload."
fi
tengine_port="${target_tengine_port}"
web_root="${target_web_root}"
log_dir="${target_log_dir}"
php_fpm_socket="${target_php_fpm_socket}"
run_user="${target_run_user}"
run_group="${target_run_group}"
verify_runtime_permissions
persist_install_parameters
rm -f -- "${path_acl_transaction_file}"
path_acl_transaction_file=""
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "Tengine 配置已生效"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
