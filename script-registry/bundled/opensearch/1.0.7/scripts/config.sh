#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"
load_install_parameters

revision() {
  { sha256sum "${config_file}"; sha256sum "${jvm_options_file}"; } | sha256sum | awk '{print $1}'
}
config_value() {
  local key="$1" fallback="$2" value
  value="$(sed -nE "s/^[[:space:]]*${key}:[[:space:]]*(.*)[[:space:]]*$/\\1/p" "${config_file}" | tail -n1)"
  value="${value%%#*}"; value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value:-${fallback}}"
}
prune_backups() {
  local -a backups=(); local index
  mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null | sort -rn | cut -d' ' -f2-)
  for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
}

managed_installation_present || die "Managed OpenSearch configuration is unavailable."
[[ -f "${config_file}" && -f "${jvm_options_file}" ]] || die "Managed OpenSearch configuration files are missing."
grep -Fqx '# BEGIN ONEINSTACK MANAGED' "${config_file}" || die "EXTERNAL_CONFIGURATION_DETECTED: Oneinstack managed block is missing."

if [[ "${operation}" == get ]]; then
  opensearch_port="$(config_value 'http\.port' "${opensearch_port}")"
  bind_address="$(config_value 'network\.host' "${bind_address}")"
  cluster_name="$(config_value 'cluster\.name' "${cluster_name}")"
  node_name="$(config_value 'node\.name' "${node_name}")"
  memory_lock="$(config_value 'bootstrap\.memory_lock' false)"
  heap_size_mb="$(sed -nE 's/^-Xms([0-9]+)m$/\1/p' "${jvm_options_file}" | head -n1)"
  [[ -n "${heap_size_mb}" ]] || heap_size_mb=1024
  password_configured=false; [[ -f "${state_dir}/password-configured" ]] && password_configured=true
  systemd_state=inactive; service_is_active && systemd_state=active
  https_state=unavailable; https_ready && https_state=ready
  printf 'component=opensearch\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'httpPort=%s\nbindAddress=%s\nclusterName=%s\nnodeName=%s\nheapSizeMB=%s\nmemoryLock=%s\n' \
    "${opensearch_port}" "${bind_address}" "${cluster_name}" "${node_name}" "${heap_size_mb}" "${memory_lock}"
  printf 'runtime.port=%s\nruntime.bindAddress=%s\nruntime.installDir=%s\nruntime.dataDir=%s\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\n' \
    "${opensearch_port}" "${bind_address}" "${install_dir}" "${data_dir}" "${log_dir}" "${run_user}" "${run_group}"
  printf 'runtime.configFile=%s\nruntime.serviceName=%s\nruntime.version=%s\nruntime.httpsState=%s\nruntime.systemdState=%s\n' \
    "${config_file}" "${service_name}" "$(actual_version)" "${https_state}" "${systemd_state}"
  printf 'connection.port=%s\nconnection.bindAddress=%s\nconnection.username=admin\nconnection.passwordConfigured=%s\n' \
    "${opensearch_port}" "${bind_address}" "${password_configured}"
  exit 0
fi

[[ "${operation}" == apply ]] || die "Unsupported OpenSearch configuration operation."
require_root
validate_inputs
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die "Invalid OpenSearch configuration revision."
[[ "$(revision)" == "${expected_revision}" ]] || { printf 'Configuration changed since preview; refresh and try again.\n' >&2; exit 75; }
target_port="${ONEINSTACK_CONFIG_HTTP_PORT:-}"
target_bind="${ONEINSTACK_CONFIG_BIND_ADDRESS:-}"
target_cluster="${ONEINSTACK_CONFIG_CLUSTER_NAME:-}"
target_node="${ONEINSTACK_CONFIG_NODE_NAME:-}"
target_heap="${ONEINSTACK_CONFIG_HEAP_SIZE_MB:-}"
target_memory_lock="${ONEINSTACK_CONFIG_MEMORY_LOCK:-}"
[[ "${target_port}" =~ ^[0-9]+$ && "${target_port}" -ge 1 && "${target_port}" -le 65535 ]] || die "Invalid httpPort."
validate_bind_address "${target_bind}"; validate_name "${target_cluster}" clusterName; validate_name "${target_node}" nodeName
[[ "${target_heap}" =~ ^[0-9]+$ && "${target_heap}" -ge 512 && "${target_heap}" -le 1048576 ]] || die "heapSizeMB must be 512-1048576."
[[ "${target_memory_lock}" == true || "${target_memory_lock}" == false ]] || die "memoryLock must be true or false."
current_port="$(config_value 'http\.port' "${opensearch_port}")"
if [[ "${target_port}" != "${current_port}" ]] && port_is_listening "${target_port}"; then die "PORT_CONFLICT: target OpenSearch port ${target_port} is occupied."; fi

opensearch_port="${target_port}"; bind_address="${target_bind}"; cluster_name="${target_cluster}"; node_name="${target_node}"
heap_size_mb="${target_heap}"; memory_lock="${target_memory_lock}"
config_candidate="$(mktemp "${install_dir}/config/.opensearch.yml.XXXXXX")"
jvm_candidate="$(mktemp "${install_dir}/config/jvm.options.d/.oneinstack.options.XXXXXX")"
write_managed_config "${config_candidate}" "${config_file}"
write_jvm_options "${jvm_candidate}"
grep -Fqx 'discovery.type: single-node' "${config_candidate}" || die "OpenSearch single-node discovery invariant is missing."
if ! grep -Fqx -- "-Xms${heap_size_mb}m" "${jvm_candidate}" || ! grep -Fqx -- "-Xmx${heap_size_mb}m" "${jvm_candidate}"; then
  die "OpenSearch JVM heap candidate is invalid."
fi
install -d -m 0700 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${config_file}" "${backup_dir}/opensearch.yml"; cp -a -- "${jvm_options_file}" "${backup_dir}/oneinstack.options"
cp -a -- "${install_parameters_file}" "${backup_dir}/install-parameters"
was_active=false; service_is_active && was_active=true
committed=false
rollback_config() {
  local code="${1:-$?}"; set +e
  if [[ "${committed}" == true ]]; then
    cp -a -- "${backup_dir}/opensearch.yml" "${config_file}"; cp -a -- "${backup_dir}/oneinstack.options" "${jvm_options_file}"
    cp -a -- "${backup_dir}/install-parameters" "${install_parameters_file}"
    if [[ "${was_active}" == true ]]; then service_restart; else service_stop; fi
  fi
  rm -f -- "${config_candidate:-}" "${jvm_candidate:-}"; exit "${code}"
}
trap 'rollback_config $?' ERR; trap 'rollback_config 130' INT; trap 'rollback_config 143' TERM
emit_progress 55 config_publish "正在原子发布 OpenSearch 配置"
mv -f -- "${config_candidate}" "${config_file}"; mv -f -- "${jvm_candidate}" "${jvm_options_file}"
chown "${run_user}:${run_group}" "${config_file}" "${jvm_options_file}"; committed=true
persist_install_parameters
if [[ "${was_active}" == true ]]; then
  service_restart
  if ! wait_for_service || ! wait_for_https; then die "OpenSearch did not become HTTPS-ready after configuration restart."; fi
  verify_certificate_health || die "OpenSearch cluster health failed after configuration restart."
else
  service_stop
fi
trap - ERR INT TERM
prune_backups
emit_progress 100 config_applied "OpenSearch 配置已生效"
printf 'Configuration backup: %s\n' "$(basename -- "${backup_dir}")"
