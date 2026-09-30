#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${config_backup_root}"

apache_config_php_socket() {
  sed -nE 's/.*proxy:unix:([^|]+)\|fcgi.*/\1/p' "${managed_config_file}" | head -n1
}

apache_config_web_root() {
  local value
  value="$(sed -nE 's/^[[:space:]]*DocumentRoot[[:space:]]+"([^"]+)\/default".*/\1/p' "${managed_config_file}" | head -n1)"
  apache_normalize_web_root "${value:-${web_root}}"
}

apache_config_log_dir() {
  sed -nE 's|^[[:space:]]*ErrorLog[[:space:]]+"([^\"]+)/apache-error\.log".*|\1|p' "${managed_config_file}" | head -n1
}

apache_config_prune_backups() {
  local -a backups=()
  local index
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  for ((index=20; index<${#backups[@]}; index++)); do
    rm -rf -- "${backups[index]}"
  done
}

require_root
validate_inputs
check_host
apache_require_owned_installation
[[ -f "${config_file}" && -f "${managed_config_file}" && -x "${install_dir}/bin/httpd" ]] ||
  die "Apache configuration is unavailable."

current_port="$(apache_runtime_port)"
current_workers="$(apache_config_value MaxRequestWorkers 256)"
current_keepalive="$(apache_config_value KeepAliveTimeout 5)"
current_php_socket="$(apache_config_php_socket)"
current_web_root="$(apache_config_web_root)"
current_log_dir="$(apache_config_log_dir)"
[[ -n "${current_php_socket}" ]] || current_php_socket="${php_fpm_socket}"
[[ -n "${current_log_dir}" ]] || current_log_dir="${log_dir}"

if [[ "${operation}" == get ]]; then
  printf 'component=apache\nrevision=%s\napply_mode=restart\n' "$(apache_revision)"
  printf 'port=%s\nmaxRequestWorkers=%s\nkeepaliveTimeout=%s\nphpFpmSocket=%s\nwebRoot=%s\nlogDir=%s\n' \
    "${current_port}" "${current_workers}" "${current_keepalive}" "${current_php_socket}" \
    "${current_web_root}" "${current_log_dir}"
  printf 'runtime.bindAddress=0.0.0.0\nruntime.port=%s\nruntime.installDir=%s\nruntime.dataDir=\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\n' \
    "${current_port}" "${install_dir}" "${current_log_dir}" "${run_user}" "${run_group}"
  exit 0
fi

[[ "${operation}" == apply ]] || die "Unsupported configuration operation."
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid configuration revision."
[[ "$(apache_revision)" == "${expected_revision}" ]] || {
  printf 'Configuration changed since preview; refresh and try again.\n' >&2
  exit 75
}
APACHE_ALLOW_OWN_PORT=true apache_check_web_server_conflicts
apache_service_active || die "Apache must be active before applying configuration."

target_port="${ONEINSTACK_CONFIG_PORT:-${current_port}}"
target_workers="${ONEINSTACK_CONFIG_MAX_REQUEST_WORKERS:-${current_workers}}"
target_keepalive="${ONEINSTACK_CONFIG_KEEPALIVE_TIMEOUT:-${current_keepalive}}"
target_php_socket="${ONEINSTACK_CONFIG_PHP_FPM_SOCKET:-${current_php_socket}}"
target_web_root="$(apache_normalize_web_root "${ONEINSTACK_CONFIG_WEB_ROOT:-${current_web_root}}")"
target_log_dir="${ONEINSTACK_CONFIG_LOG_DIR:-${current_log_dir}}"

[[ "${target_port}" =~ ^[0-9]+$ && "${target_port}" -ge 1 && "${target_port}" -le 65535 ]] || die "Invalid port."
[[ "${target_workers}" =~ ^[0-9]+$ && "${target_workers}" -ge 1 && "${target_workers}" -le 65535 ]] || die "Invalid maxRequestWorkers."
[[ "${target_keepalive}" =~ ^[0-9]+$ && "${target_keepalive}" -ge 1 && "${target_keepalive}" -le 600 ]] || die "Invalid keepaliveTimeout."
apache_validate_path "${target_php_socket}" PHP_FPM_SOCKET
apache_validate_web_root "${target_web_root}"
apache_validate_log_dir "${target_log_dir}"
apache_validate_vhost_port_change "${current_port}" "${target_port}"
if [[ "${target_port}" != "${current_port}" ]]; then
  [[ -z "$(ss -H -ltn "sport = :${target_port}" 2>/dev/null)" ]] || die "PORT_IN_USE: TCP port ${target_port} is already occupied."
fi
install -d -m 0755 -- "${target_web_root}" "${target_web_root}/default"
install -d -m 0750 -- "${target_log_dir}"
chown "${run_user}:${run_group}" "${target_web_root}" "${target_web_root}/default" "${target_log_dir}" 2>/dev/null || true

install -d -m 0750 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -a -- "${config_file}" "${backup_dir}/httpd.conf"
cp -a -- "${managed_config_file}" "${backup_dir}/oneinstack.conf"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"

managed_candidate="$(mktemp "${managed_config_file}.XXXXXX")"
main_candidate="$(mktemp "${config_file}.XXXXXX")"
apache_render_managed_config "${managed_candidate}" "${target_port}" "${target_workers}" "${target_keepalive}" \
  "${target_php_socket}" "${target_web_root}" "${target_log_dir}"
cp -p -- "${config_file}" "${main_candidate}"
awk -v port="${target_port}" 'BEGIN { replaced=0 } /^[[:space:]]*Listen[[:space:]]+/ && replaced == 0 { print "Listen " port; replaced=1; next } { print } END { if (replaced == 0) print "Listen " port }' "${main_candidate}" >"${main_candidate}.listen"
mv -f -- "${main_candidate}.listen" "${main_candidate}"

managed_include="IncludeOptional ${managed_config_file}"
vhost_include="IncludeOptional ${web_vhost_root}/apache/*.conf"
if ! grep -Fqx -- "${managed_include}" "${main_candidate}"; then
  printf '%s\n' "${managed_include}" >>"${main_candidate}"
fi
if ! grep -Fqx -- "${vhost_include}" "${main_candidate}"; then
  printf '%s\n' "${vhost_include}" >>"${main_candidate}"
fi
sed "s|${managed_config_file}|${managed_candidate}|g" "${main_candidate}" >"${main_candidate}.validate"

emit_progress 35 config_validate "Validating Apache candidate configuration"
if ! "${install_dir}/bin/httpd" -t -f "${main_candidate}.validate"; then
  rm -f -- "${managed_candidate}" "${main_candidate}" "${main_candidate}.validate"
  die "Apache candidate configuration is invalid."
fi

committed=false
rollback_config() {
  local code="${1:-$?}" restore_main restore_managed
  trap - ERR INT TERM
  set +e
  if [[ "${committed}" == true ]]; then
    restore_main="$(mktemp "${config_file}.restore.XXXXXX")"
    restore_managed="$(mktemp "${managed_config_file}.restore.XXXXXX")"
    cp -p -- "${backup_dir}/httpd.conf" "${restore_main}"
    cp -p -- "${backup_dir}/oneinstack.conf" "${restore_managed}"
    mv -f -- "${restore_main}" "${config_file}"
    mv -f -- "${restore_managed}" "${managed_config_file}"
    if systemctl restart "${service_name}.service" 2>/dev/null; then
      (apache_wait_service) >/dev/null 2>&1 || true
      (apache_verify_runtime) >/dev/null 2>&1 || true
    fi
  fi
  rm -f -- "${managed_candidate}" "${main_candidate}" "${main_candidate}.validate"
  exit "${code}"
}
trap 'rollback_config $?' ERR
trap 'rollback_config 130' INT
trap 'rollback_config 143' TERM

emit_progress 60 config_publish "Publishing Apache configuration atomically"
chmod --reference="${managed_config_file}" "${managed_candidate}"
chown --reference="${managed_config_file}" "${managed_candidate}"
chmod --reference="${config_file}" "${main_candidate}"
chown --reference="${config_file}" "${main_candidate}"
committed=true
mv -f -- "${managed_candidate}" "${managed_config_file}"
mv -f -- "${main_candidate}" "${config_file}"
"${install_dir}/bin/httpd" -t -f "${config_file}"

emit_progress 78 config_restart "Restarting Apache"
systemctl restart "${service_name}.service"
apache_wait_service
apache_port="${target_port}"
php_fpm_socket="${target_php_socket}"
web_root="${target_web_root}"
log_dir="${target_log_dir}"
apache_verify_runtime
apache_persist_install_parameters
apache_write_state

trap - ERR INT TERM
rm -f -- "${managed_candidate}" "${main_candidate}" "${main_candidate}.validate"
apache_config_prune_backups
emit_progress 100 config_applied "Apache configuration applied"
printf 'Configuration backup: %s\n' "$(basename "${backup_dir}")"
