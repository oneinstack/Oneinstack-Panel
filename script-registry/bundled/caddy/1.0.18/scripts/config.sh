#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
load_persisted_parameters
validate_inputs
[[ -x "${binary}" && -r "${caddyfile}" && -r "${managed_config}" ]] || die_code CADDY_CONFIG_MISSING "Caddy managed configuration is unavailable."

read_actual_configuration() {
  local detected
  detected="$(sed -nE 's|^[[:space:]]*http://:([0-9]+)[[:space:]]*\{[[:space:]]*$|\1|p' "${managed_config}" | head -n1)"
  [[ -z "${detected}" ]] || caddy_port="${detected}"
  detected="$(sed -nE 's|^[[:space:]]*root[[:space:]]+\*[[:space:]]+(.+)/default[[:space:]]*$|\1|p' "${managed_config}" | head -n1)"
  [[ -z "${detected}" ]] || web_root="${detected}"
  detected="$(sed -nE 's|^[[:space:]]*php_fastcgi[[:space:]]+unix//(.+)[[:space:]]*$|/\1|p' "${managed_config}" | head -n1)"
  [[ -z "${detected}" ]] || php_fpm_socket="${detected}"
  detected="$(sed -nE 's|^[[:space:]]*output[[:space:]]+file[[:space:]]+(.+)/caddy/caddy-access\.log[[:space:]]*$|\1|p' "${managed_config}" | head -n1)"
  [[ -z "${detected}" ]] || log_dir="${detected}"
}

read_actual_configuration
if [[ "${operation}" == "get" ]]; then
  printf 'component=caddy\nrevision=%s\napply_mode=reload\n' "$(config_revision)"
  printf 'port=%s\nphpFpmSocket=%s\nwebRoot=%s\nlogDir=%s\n' "${caddy_port}" "${php_fpm_socket}" "${web_root}" "${log_dir}"
  printf 'runtime.port=%s\nruntime.bindAddress=0.0.0.0\nruntime.socketPath=%s\n' "${caddy_port}" "${php_fpm_socket}"
  printf 'runtime.installDir=%s\nruntime.dataDir=%s\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\n' \
    "${install_dir}" "${data_dir}" "${log_dir}" "${run_user}" "${run_group}"
  printf 'runtime.configFile=%s\nruntime.vhostDir=%s\nruntime.serviceName=%s\nruntime.version=%s\n' \
    "${caddyfile}" "${vhost_dir}" "${service_name}" "$(current_version)"
  exit 0
fi

[[ "${operation}" == "apply" ]] || die_code CADDY_CONFIG_OPERATION_INVALID "Unsupported configuration operation ${operation}."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die_code CADDY_CONFIG_REVISION_INVALID "Configuration revision must be a SHA-256 value."
[[ "$(config_revision)" == "${expected_revision}" ]] || { printf 'CADDY_CONFIG_REVISION_CONFLICT: configuration changed since preview.\n' >&2; exit 75; }

old_port="${caddy_port}"
target_port="${ONEINSTACK_CONFIG_PORT:-${caddy_port}}"
target_socket="${ONEINSTACK_CONFIG_PHP_FPM_SOCKET:-${php_fpm_socket}}"
target_web_root="${ONEINSTACK_CONFIG_WEB_ROOT:-${web_root}}"
target_log_dir="${ONEINSTACK_CONFIG_LOG_DIR:-${log_dir}}"
[[ "${target_port}" =~ ^[0-9]+$ && "${target_port}" -ge 1 && "${target_port}" -le 65535 ]] || die_code CADDY_INVALID_PORT "Invalid port."
validate_path "${target_socket}" PHP_FPM_SOCKET
validate_path "${target_web_root}" WEB_ROOT
validate_path "${target_log_dir}" LOG_DIR
if [[ "${target_port}" != "${old_port}" ]]; then
  caddy_port="${target_port}"
  port_listening && die_code CADDY_PORT_IN_USE "TCP port ${target_port} is already listening."
  caddy_port="${old_port}"
fi

install -d -m 0700 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${caddyfile}" "${backup_dir}/Caddyfile"
cp -a -- "${managed_config}" "${backup_dir}/oneinstack-default.caddy"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"
path_acl_transaction_file="${backup_dir}/path-acl-added"
: >"${path_acl_transaction_file}"
chmod 0600 "${path_acl_transaction_file}"

committed=false
restore_configuration() {
  local code="$1"
  trap - EXIT ERR INT TERM
  rm -f -- "${candidate:-}" "${candidate_main:-}"
  if [[ "${committed}" == true ]]; then
    cp -a -- "${backup_dir}/oneinstack-default.caddy" "${managed_config}"
    reload_managed_caddy || true
  fi
  restore_path_acl_transaction "${path_acl_transaction_file}"
  exit "${code}"
}
trap 'restore_configuration $?' EXIT
trap 'restore_configuration 130' INT
trap 'restore_configuration 143' TERM

prepare_log_directory "${target_log_dir}"
prepare_web_directory "${target_web_root}"

candidate="$(mktemp "${config_dir}/.oneinstack-default.caddy.XXXXXX")"
candidate_main="$(mktemp "${config_dir}/.Caddyfile.XXXXXX")"
render_managed_config "${candidate}" "${target_port}" "${target_socket}" "${target_web_root}" "${target_log_dir}"
render_main_config "${candidate_main}" "${candidate}"
emit_progress 35 config_validate "Validating the Caddy candidate configuration as caddy:caddy"
if ! validate_caddy_config "${candidate_main}"; then
  rm -f -- "${candidate}" "${candidate_main}"
  die_code CADDY_CONFIG_INVALID "Caddy candidate configuration validation failed."
fi
rm -f -- "${candidate_main}"
emit_progress 65 config_publish "Atomically publishing the Caddy managed configuration"
mv -f -- "${candidate}" "${managed_config}"
committed=true
if active_unit "${service_name}.service"; then
  emit_progress 82 config_reload "Reloading Caddy"
  reload_managed_caddy
  active_unit "${service_name}.service" || die_code CADDY_CONFIG_RELOAD_FAILED "Caddy became inactive after configuration reload."
  caddy_port="${target_port}"
  wait_for_listener 15 || die_code CADDY_CONFIG_RELOAD_FAILED "Caddy did not open the configured port ${target_port} after reload."
fi
caddy_port="${target_port}"
php_fpm_socket="${target_socket}"
web_root="${target_web_root}"
log_dir="${target_log_dir}"
detect_platform
persist_install_parameters
write_installed_state
rm -f -- "${path_acl_transaction_file}"
path_acl_transaction_file=""
trap - EXIT ERR INT TERM
mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -print | sort -r)
for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
emit_progress 100 config_applied "Caddy configuration was applied by reload."
printf 'Configuration backup: %s\n' "$(basename -- "${backup_dir}")"
