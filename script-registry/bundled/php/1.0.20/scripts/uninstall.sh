#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ -f "${state_dir}/version" ]] ||
  die_code COMPONENT_NOT_MANAGED "Managed PHP state is missing; refusing to remove unowned resources"
managed_installation_present || die_code COMPONENT_NOT_MANAGED "Managed PHP installation is not present"
removed_dir="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)"
install -d -m 0750 -- "${removed_dir}"
stop_managed_service
if systemd_available && managed_unit_matches; then
  systemctl disable "${service_name}.service" 2>/dev/null || true
fi
[[ ! -e "${install_dir}" ]] || mv -- "${install_dir}" "${removed_dir}/install"
if [[ -f "${unit_file}" ]] && managed_unit_matches; then
  mv -- "${unit_file}" "${removed_dir}/${service_name}.service"
fi
if systemd_available; then systemctl daemon-reload; fi
{
  printf 'component=php\n'
  printf 'action=uninstall\n'
  printf 'data_policy=%s\n' "${data_policy}"
  printf 'delete_confirmed=%s\n' "${delete_data_confirm}"
  printf 'result=completed\n'
} >"${state_dir}/uninstall-audit"
chmod 0600 "${state_dir}/uninstall-audit"
rm -f -- "${state_dir}/version" "${state_dir}/patch-version" "${state_dir}/pending-version" "${state_dir}/pending-patch-version" "${state_dir}/pending-runtime-params" "${state_dir}/managed" "${state_dir}/php-fpm.pid"
[[ ! -e "${removed_dir}/install" ]] || safe_remove_component_path "${removed_dir}/install"
[[ ! -e "${removed_dir}/${service_name}.service" ]] || safe_remove_component_path "${removed_dir}/${service_name}.service"
safe_remove_component_path "${removed_dir}"
if [[ "${data_policy}" == "delete" ]]; then
  safe_remove_component_path "${rollback_dir}"
fi
echo "component=php"
echo "action=uninstall"
echo "data_policy=${data_policy}"
echo "result=completed"
