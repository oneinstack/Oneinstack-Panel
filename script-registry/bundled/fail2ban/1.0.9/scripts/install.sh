#!/usr/bin/env bash
set -Eeuo pipefail
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
source "${script_dir}/common.sh"

require_root
validate_inputs
check_host
install -d -m 0750 -- "${state_dir}"
if [[ -f "${state_dir}/installed.json" ]] && package_installed &&
  [[ -x "${helper_path}" && -r "${action_path}" && -r "${filter_path}" &&
    -x "${redis_acl_helper}" && -r "${redis_auth_filter_path}" && -r "${defaults_path}" &&
    -r "${redis_acl_unit}" ]] && systemctl is-active --quiet "${redis_acl_service}"; then
  current_runtime_version="$(runtime_version)"
  if [[ "${software_version}" == "system" || "${current_runtime_version}" == "${software_version}" ]]; then
    emit_progress 100 already_installed "Managed Fail2ban installation is healthy"
    exit 0
  fi
fi
snapshot_existing
if [[ -f "${migration_dir}/package-existed" ]]; then
  emit_progress 30 migration.package.reinstalling "Reinstalling existing Fail2ban under component management"
  reinstall_package
else
  if [[ "${software_version}" == "system" ]]; then
    emit_progress 20 install_package "正在通过系统包管理器安装 Fail2ban"
  else
    emit_progress 20 install_package "正在安装并校验 Fail2ban ${software_version} 上游运行时"
  fi
  install_package
fi
: >"${installed_marker}"
require_command fail2ban-client
require_command python3

emit_progress 60 install_integration "正在安装 OneinStack 受控事件上报集成"
install -d -m 0750 -- "${event_root}" "$(dirname -- "${helper_path}")"
touch -- "${event_file}" "${manual_log}"
chmod 0640 "${event_file}" "${manual_log}"
install -m 0750 -- "${script_dir}/../files/oneinstack-fail2ban-report.py" "${helper_path}"
install -m 0640 -- "${script_dir}/../files/oneinstack-report.conf" "${action_path}"
install -m 0640 -- "${script_dir}/../files/oneinstack-manual.conf" "${filter_path}"
install -m 0750 -- "${script_dir}/../files/oneinstack-redis-acl-log.py" "${redis_acl_helper}"
install -m 0640 -- "${script_dir}/../files/oneinstack-redis-auth.conf" "${redis_auth_filter_path}"
: >"${integration_marker}"
write_default_configuration
start_redis_acl_collector
write_state

emit_progress 80 validate_configuration "正在校验 Fail2ban 配置"
fail2ban-client -t
validate_runtime_version
emit_progress 90 start_service "正在启动 Fail2ban 服务"
service_enable_start
wait_for_service_ready
emit_progress 100 install_completed "Fail2ban 安装并验证完成"
echo "Fail2ban installed with the constrained OneinStack integration."
