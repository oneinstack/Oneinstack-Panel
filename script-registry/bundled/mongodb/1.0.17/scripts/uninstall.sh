#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] || die "Managed MongoDB state is missing; refusing to remove unowned resources."
managed_installation_present || die "Managed MongoDB ownership cannot be verified."
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
delete_confirm="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] || die "UNINSTALL_DATA_POLICY must be preserve or delete."
[[ "${data_policy}" != delete || "${delete_confirm}" == true ]] || die "Deleting MongoDB data requires data-policy=delete and delete-data-confirm=true."
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0700 -- "${removed_dir}"
for name in version source-sha256 install-parameters password-configured; do
  [[ ! -e "${state_dir}/${name}" ]] || cp -a -- "${state_dir}/${name}" "${removed_dir}/${name}"
done
service_stop
systemctl disable mongod.service 2>/dev/null || true
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/mongod.service"
[[ ! -e "${config_file}" ]] || mv -- "${config_file}" "${removed_dir}/mongod.conf"
systemctl daemon-reload; systemctl reset-failed mongod.service 2>/dev/null || true
rm -f -- "${state_dir}/version" "${state_dir}/source-sha256" "${state_dir}/pending-version" "${state_dir}/pending-source-sha256" "${state_dir}/install-parameters" "${state_dir}/password-configured"
if [[ "${data_policy}" == delete ]]; then
  rm -rf -- "${data_dir}"
  [[ "${log_dir}" == "${data_dir}" ]] || rm -rf -- "${log_dir}"
  rm -f -- "${retained_data_file}"
else
  marker="$(mktemp "${state_dir}/.retained-data.XXXXXX")"
  printf '%s\n' "${removed_dir##*/}" >"${marker}"
  chmod 0600 "${marker}"
  mv -f -- "${marker}" "${retained_data_file}"
fi
remove_managed_path_acl_entries "${managed_path_acl_file}"
rm -f -- "${managed_path_acl_file}"
emit_progress 100 uninstall_completed "MongoDB 已卸载，数据策略为 ${data_policy}"
