#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
managed_openresty_state_exists ||
  die "Managed OpenResty state is missing; refusing to remove unowned resources."
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
confirm_delete="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
case "${data_policy}" in
  preserve) ;;
  delete)
    [[ "${confirm_delete}" == "true" ]] || die "Deleting OpenResty component state requires explicit confirmation."
    ;;
  *) die "UNINSTALL_DATA_POLICY must be preserve or delete." ;;
esac
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
if openresty_is_running; then
  stop_openresty
fi
systemd_available && systemctl disable "${service_name}.service" 2>/dev/null || true
if [[ "${data_policy}" == "preserve" ]]; then
  snapshot_managed_openresty_config
fi
remove_managed_path_acl_entries "${managed_path_acl_file}"
rm -f -- "${managed_path_acl_file}"
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
[[ ! -e "${unit_file}" ]] || mv -- "${unit_file}" "${removed_dir}/openresty.service"
systemd_available && systemctl daemon-reload
rm -f -- "${state_dir}/version" "${state_dir}/pending-version"
if [[ "${data_policy}" == "delete" ]]; then
  rm -rf -- "${removed_dir}"
  rm -rf -- "${state_dir}"
  echo "OpenResty removed. Website root ${web_root} and log directory ${log_dir} were preserved."
else
  echo "OpenResty removed. Website data and virtual-host configuration were preserved in ${removed_dir}."
fi
