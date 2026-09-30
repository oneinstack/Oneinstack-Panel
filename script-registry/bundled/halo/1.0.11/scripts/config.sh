#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
managed_installation_present || die "MANAGED_STATE_MISSING: Halo managed configuration is unavailable."

if [[ "${operation}" == get ]]; then
  password_configured=false; [[ -s "${environment_file}" ]] && password_configured=true
  printf 'component=halo\nrevision=%s\napply_mode=restart\n' "$(config_revision)"
  printf 'haloPort=%s\nbindAddress=%s\nexternalUrl=%s\nuseAbsolutePermalink=%s\nforwardHeadersStrategy=%s\n' \
    "${halo_port}" "${bind_address}" "${external_url}" "${use_absolute_permalink}" "${forward_headers_strategy}"
  printf 'compressionEnabled=%s\nstaticCacheMaxAgeDays=%s\njvmXmsMb=%s\njvmXmxMb=%s\n' \
    "${compression_enabled}" "${static_cache_max_age_days}" "${jvm_xms_mb}" "${jvm_xmx_mb}"
  printf 'logMaxFileSizeMB=%s\nlogTotalSizeCapMB=%s\nlogMaxHistory=%s\n' \
    "${log_max_file_size_mb}" "${log_total_size_cap_mb}" "${log_max_history}"
  printf 'connection.databaseType=%s\nconnection.passwordConfigured=%s\n' "${database_type}" "${password_configured}"
  exit 0
fi

[[ "${operation}" == apply ]] || die "INVALID_CONFIG_OPERATION: expected get or apply."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "INVALID_CONFIG_REVISION: revision must be a SHA-256 value."
[[ "$(config_revision)" == "${expected_revision}" ]] || { printf 'Configuration changed since preview; refresh and try again.\n' >&2; exit 75; }

old_port="${halo_port}"
halo_port="${ONEINSTACK_CONFIG_HALO_PORT:-}"
bind_address="${ONEINSTACK_CONFIG_BIND_ADDRESS:-}"
external_url="${ONEINSTACK_CONFIG_EXTERNAL_URL:-}"
use_absolute_permalink="${ONEINSTACK_CONFIG_USE_ABSOLUTE_PERMALINK:-}"
forward_headers_strategy="${ONEINSTACK_CONFIG_FORWARD_HEADERS_STRATEGY:-}"
compression_enabled="${ONEINSTACK_CONFIG_COMPRESSION_ENABLED:-}"
static_cache_max_age_days="${ONEINSTACK_CONFIG_STATIC_CACHE_MAX_AGE_DAYS:-}"
jvm_xms_mb="${ONEINSTACK_CONFIG_JVM_XMS_MB:-}"
jvm_xmx_mb="${ONEINSTACK_CONFIG_JVM_XMX_MB:-}"
log_max_file_size_mb="${ONEINSTACK_CONFIG_LOG_MAX_FILE_SIZE_MB:-}"
log_total_size_cap_mb="${ONEINSTACK_CONFIG_LOG_TOTAL_SIZE_CAP_MB:-}"
log_max_history="${ONEINSTACK_CONFIG_LOG_MAX_HISTORY:-}"
validate_inputs
if [[ "${halo_port}" != "${old_port}" ]] && port_is_listening "${halo_port}"; then
  die "PORT_CONFLICT: target Halo port ${halo_port} is already occupied."
fi

install -d -m 0700 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${config_file}" "${backup_dir}/application.yaml"
cp -a -- "${unit_file}" "${backup_dir}/halo.service"
cp -a -- "${install_parameters_file}" "${backup_dir}/install-parameters"
was_active=false; service_is_active && was_active=true
candidate="$(mktemp "${config_dir}/.application.yaml.XXXXXX")"
write_application_config "${candidate}"
committed=false
rollback_config() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == true ]]; then
    cp -a -- "${backup_dir}/application.yaml" "${config_file}"
    cp -a -- "${backup_dir}/halo.service" "${unit_file}"
    cp -a -- "${backup_dir}/install-parameters" "${install_parameters_file}"
    systemctl daemon-reload
    if [[ "${was_active}" == true ]]; then systemctl restart "${service_name}.service"; else systemctl stop "${service_name}.service"; fi
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback_config $?' ERR
trap 'rollback_config 130' INT
trap 'rollback_config 143' TERM
emit_progress 35 config_validate "Halo 配置参数、端口和主机内存检查通过"
mv -f -- "${candidate}" "${config_file}"
committed=true
persist_install_parameters
write_service_unit
emit_progress 70 config_restart "正在重启 Halo 并验证 readiness"
service_restart
wait_for_readiness || die "READINESS_FAILED: Halo configuration did not become ready."
if [[ "${was_active}" != true ]]; then
  service_stop
  ! service_is_active || die "SERVICE_STATE_RESTORE_FAILED: Halo did not return to the previous stopped state."
fi
trap - ERR INT TERM
mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
emit_progress 100 config_applied "Halo 配置已通过 revision 乐观锁原子应用"
printf 'Configuration backup: %s\n' "$(basename -- "${backup_dir}")"
