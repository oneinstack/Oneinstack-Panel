#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_path "${state_root}" ONEINSTACK_COMPONENT_STATE
validate_path "${install_dir}" INSTALL_DIR
validate_path "${data_dir}" DATA_DIR
validate_path "${config_dir}" CONFIG_DIR
validate_account_name "${run_user}" RUN_USER
validate_account_name "${run_group}" RUN_GROUP
managed_installation_present || die "MANAGED_STATE_MISSING: refusing to remove unowned Halo resources."
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
delete_confirm="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] || die "INVALID_PARAMETER: data-policy must be preserve or delete."
validate_boolean "${delete_confirm}" delete-data-confirm
[[ "${data_policy}" != delete || "${delete_confirm}" == true ]] ||
  die "DATA_DELETE_CONFIRMATION_REQUIRED: set data-policy=delete and delete-data-confirm=true."
emit_progress 15 uninstall_service "正在停止并移除 Halo 受管 systemd 服务"
systemctl stop "${service_name}.service" 2>/dev/null || true
systemctl disable "${service_name}.service" 2>/dev/null || true
rm -f -- "${unit_file}"
systemctl daemon-reload 2>/dev/null || true
systemctl reset-failed "${service_name}.service" 2>/dev/null || true
emit_progress 45 uninstall_runtime "正在移除 Halo JAR 与私有 Temurin JRE"
rm -rf -- "${install_dir}"
if [[ "${data_policy}" == delete ]]; then
  emit_progress 70 uninstall_data "正在删除已明确确认的 Halo 数据和受保护配置"
  rm -rf -- "${data_dir}" "${config_dir}"
  rm -f -- "${retained_data_file}" "${install_parameters_file}"
else
  printf 'preserved\n' >"${retained_data_file}"
  chmod 0600 "${retained_data_file}"
  chmod 0750 "${config_dir}" 2>/dev/null || true
  chmod 0640 "${config_file}" "${environment_file}" 2>/dev/null || true
fi
remove_managed_acls
rm -f -- "${managed_acl_file}" "${state_dir}/installed" "${state_dir}/ownership" "${state_dir}/version" \
  "${state_dir}/package-version" "${state_dir}/pending-version" "${state_dir}/pending-jar-sha256"
rm -rf -- "${rollback_dir}" "${state_dir}/config-backups"
if [[ "${data_policy}" == delete ]]; then
  if [[ -f "${state_dir}/created-user" ]]; then userdel "${run_user}" 2>/dev/null || true; rm -f -- "${state_dir}/created-user"; fi
  if [[ -f "${state_dir}/created-group" ]]; then groupdel "${run_group}" 2>/dev/null || true; rm -f -- "${state_dir}/created-group"; fi
  rmdir "${state_dir}" 2>/dev/null || true
  printf 'Halo uninstalled; managed data and protected configuration were deleted after explicit confirmation.\n'
else
  printf 'Halo uninstalled; data and protected database configuration were preserved at %s and %s.\n' "${data_dir}" "${config_dir}"
fi
printf 'External database contents were not modified.\n'
emit_progress 100 uninstall_completed "Halo 卸载完成，外部数据库从未删除"
