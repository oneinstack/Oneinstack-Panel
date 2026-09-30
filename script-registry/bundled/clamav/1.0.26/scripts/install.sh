#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_install
  exit 0
fi
validate_inputs
if [[ "${install_mode}" == offline ]]; then
  validate_offline_bundle
fi
if is_managed; then
  load_managed_state
  if is_healthy; then
    emit_progress 100 already_installed 'ClamAV 已由 Oneinstack 管理且健康，跳过重复安装'
    exit 0
  fi
fi
if [[ ! -d "${migration_dir}" && ! -d "${external_dir}" ]] && find_existing >/dev/null; then
  snapshot_existing
fi
completed=false
rollback_on_error() {
  local code="${1:-$?}"
  set +e
  [[ "${completed}" == true ]] || rollback_installation
  exit "${code}"
}
trap 'rollback_on_error $?' ERR
trap 'rollback_on_error 130' INT
trap 'rollback_on_error 143' TERM

emit_progress 15 packages.installing '正在安装与当前系统 profile 匹配的 ClamAV 原生包'
install_packages
resolve_runtime_identity
disable_native_units
install -d -m 0750 -- "${state_dir}" "${config_dir}" "${data_dir}" "${log_dir}"
update_mode=auto
[[ "${install_mode}" == center ]] || update_mode=manual
write_settings "${settings_file}" "${update_mode}" 12 database.clamav.net 10 100 400 100 16 10000 no
write_clamd_config "${clamd_config}" "${socket_path}" 10 100 400 100 16 10000 no
write_freshclam_config "${freshclam_config}" 12 database.clamav.net
configure_runtime_permissions
validate_freshclam_config
write_systemd_units "${update_mode}" 12

if [[ "${install_mode}" == offline ]]; then
  emit_progress 55 database.restore '正在导入并校验离线签名病毒库'
  copy_offline_database
else
  emit_progress 55 database.update.started '正在使用受信任镜像初始化签名病毒库'
  run_initial_database_update
fi
validate_database_files "${data_dir}"
systemctl enable --now "${service_name}.service"
if [[ "${install_mode}" == center && "${update_mode}" == auto ]]; then
  systemctl enable --now "${update_timer_name}"
fi
wait_for_healthy || { health_failure_detail; die 'SERVICE_NOT_READY: ClamAV Unix Socket probe did not become healthy.'; }
commit_installation
completed=true
trap - ERR INT TERM
emit_progress 100 install_completed 'ClamAV 已安装，统一 Unix Socket 服务与病毒库探针均已验证'
