#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
revision() { sha256sum "${config_file}" | awk '{print $1}'; }
simple_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]+${key}:[[:space:]]*([^[:space:]#]+).*/\\1/p" "${config_file}" | tail -n1)"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=()
  local index
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

validate_inputs
managed_installation_present || die "MongoDB managed configuration is unavailable."
grep -Eq '^[[:space:]]+authorization:[[:space:]]+enabled[[:space:]]*$' "${config_file}" || die "AUTHENTICATION_REQUIRED: managed MongoDB authentication is not enabled."
if [[ "${operation}" == get ]]; then
  effective_port="$(config_scalar net port "${mongodb_port}")"
  effective_bind="$(config_scalar net bindIp "${mongodb_bind_ip}")"
  max_connections="$(config_scalar net maxIncomingConnections 0)"
  cache_gb="$(simple_value cacheSizeGB 0)"
  profile_mode="$(config_scalar operationProfiling mode off)"
  slow_ms="$(config_scalar operationProfiling slowOpThresholdMs 100)"
  password_configured=false; [[ -f "${state_dir}/password-configured" ]] && password_configured=true
  printf 'component=mongodb\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'mongodbPort=%s\nbindIp=%s\nmaxIncomingConnections=%s\nwiredTigerCacheSizeGB=%s\noperationProfilingMode=%s\nslowOpThresholdMs=%s\n' \
    "${effective_port}" "${effective_bind}" "${max_connections}" "${cache_gb}" "${profile_mode}" "${slow_ms}"
  printf 'runtime.port=%s\nruntime.bindAddress=%s\nruntime.installDir=%s\nruntime.dataDir=%s\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\n' \
    "${effective_port}" "${effective_bind}" "${install_dir}" "${data_dir}" "${log_dir}" "${run_user}" "${run_group}"
  printf 'runtime.configFile=%s\nruntime.serviceName=mongod\nruntime.version=%s\n' "${config_file}" "$(actual_version)"
  printf 'connection.port=%s\nconnection.bindAddress=%s\nconnection.username=%s\nconnection.passwordConfigured=%s\n' \
    "${effective_port}" "${effective_bind}" "${admin_username}" "${password_configured}"
  exit 0
fi

[[ "${operation}" == apply ]] || die "Unsupported MongoDB configuration operation."
require_root
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid MongoDB configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || { printf 'Configuration changed since preview; refresh and try again.\n' >&2; exit 75; }
target_port="${ONEINSTACK_CONFIG_MONGODB_PORT:-}"
target_bind="${ONEINSTACK_CONFIG_BIND_IP:-}"
max_connections="${ONEINSTACK_CONFIG_MAX_INCOMING_CONNECTIONS:-}"
cache_gb="${ONEINSTACK_CONFIG_WIREDTIGER_CACHE_SIZE_GB:-}"
profile_mode="${ONEINSTACK_CONFIG_OPERATION_PROFILING_MODE:-}"
slow_ms="${ONEINSTACK_CONFIG_SLOW_OP_THRESHOLD_MS:-}"
[[ "${target_port}" =~ ^[0-9]+$ && "${target_port}" -ge 1 && "${target_port}" -le 65535 ]] || die "Invalid mongodbPort."
validate_bind_ip "${target_bind}"
[[ "${max_connections}" =~ ^[0-9]+$ && ( "${max_connections}" -eq 0 || "${max_connections}" -ge 100 ) && "${max_connections}" -le 1000000 ]] || die "maxIncomingConnections must be 0 or 100-1000000."
[[ "${cache_gb}" =~ ^[0-9]+$ && "${cache_gb}" -ge 0 && "${cache_gb}" -le 1024 ]] || die "wiredTigerCacheSizeGB must be 0 or 1-1024."
[[ "${profile_mode}" == off || "${profile_mode}" == slowOp || "${profile_mode}" == all ]] || die "Invalid operationProfilingMode."
[[ "${slow_ms}" =~ ^[0-9]+$ && "${slow_ms}" -ge 1 && "${slow_ms}" -le 600000 ]] || die "Invalid slowOpThresholdMs."
current_port="$(config_scalar net port "${mongodb_port}")"
if [[ "${target_port}" != "${current_port}" ]] && port_is_listening "${target_port}"; then
  die "PORT_CONFLICT: target MongoDB port ${target_port} is already occupied."
fi
candidate="$(mktemp /etc/.oneinstack-mongod-config.XXXXXX)"
write_mongod_config "${candidate}" "${target_bind}" "${target_port}" enabled "${max_connections}" "${cache_gb}" "${profile_mode}" "${slow_ms}"
emit_progress 30 config_validate "正在使用 mongod --outputConfig 校验候选配置"
validate_native_config "${candidate}"
install -d -m 0700 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${config_file}" "${backup_dir}/mongod.conf"
cp -a -- "${install_parameters_file}" "${backup_dir}/install-parameters"
was_active=false; service_is_active && was_active=true
committed=false
rollback_config() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == true ]]; then
    cp -a -- "${backup_dir}/mongod.conf" "${config_file}"
    cp -a -- "${backup_dir}/install-parameters" "${install_parameters_file}"
    if [[ "${was_active}" == true ]]; then service_restart; else service_stop; fi
  fi
  rm -f -- "${candidate:-}"
  exit "${code}"
}
trap 'rollback_config $?' ERR
trap 'rollback_config 130' INT
trap 'rollback_config 143' TERM
emit_progress 62 config_publish "正在原子发布 MongoDB 配置"
mv -f -- "${candidate}" "${config_file}"
committed=true
service_restart
wait_for_mongodb "${target_port}" "${target_bind}"
if [[ "${was_active}" != true ]]; then
  service_stop
  ! service_is_active || die "MongoDB did not return to its previous stopped state."
fi
mongodb_port="${target_port}"; mongodb_bind_ip="${target_bind}"
persist_install_parameters
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "MongoDB 配置已生效"
printf 'Configuration backup: %s\n' "$(basename -- "${backup_dir}")"
