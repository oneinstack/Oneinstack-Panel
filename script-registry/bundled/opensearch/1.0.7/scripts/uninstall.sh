#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_path "${install_dir}" INSTALL_DIR; validate_path "${data_dir}" DATA_DIR; validate_path "${log_dir}" LOG_DIR; validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
managed_installation_present || die "Managed OpenSearch installation state is missing; refusing to remove unowned resources."
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
delete_confirm="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] || die "data-policy must be preserve or delete."
[[ "${delete_confirm}" == true || "${delete_confirm}" == false ]] || die "delete-data-confirm must be true or false."
if [[ "${data_policy}" == delete && "${delete_confirm}" != true ]]; then
  die "DATA_DELETE_CONFIRMATION_REQUIRED: set delete-data-confirm=true together with data-policy=delete."
fi
emit_progress 15 uninstall_service "正在停止 OpenSearch 托管服务"
systemctl disable --now "${service_name}.service" 2>/dev/null || true
rm -f -- "${unit_file}"; systemctl daemon-reload 2>/dev/null || true; systemctl reset-failed "${service_name}.service" 2>/dev/null || true
emit_progress 50 uninstall_runtime "正在删除 OpenSearch 托管程序目录"
rm -rf -- "${install_dir}"
if [[ "${data_policy}" == delete ]]; then
  emit_progress 72 uninstall_data "正在删除已明确确认的 OpenSearch 数据和日志目录"
  rm -rf -- "${data_dir}" "${log_dir}"
  rm -f -- "${install_parameters_file}"
fi
rm -f -- "${state_dir}/installed" "${state_dir}/ownership" "${state_dir}/version" "${state_dir}/pending-version" "${state_dir}/password-configured"
rm -rf -- "${rollback_dir}" "${state_dir}/config-backups"
if [[ "${data_policy}" == preserve ]]; then
  printf 'OpenSearch uninstalled; data preserved at %s and logs preserved at %s\n' "${data_dir}" "${log_dir}"
else
  rmdir "${state_dir}" 2>/dev/null || true
  printf 'OpenSearch uninstalled; explicitly confirmed data and logs were removed\n'
fi
emit_progress 100 uninstall_completed "OpenSearch 卸载完成"
