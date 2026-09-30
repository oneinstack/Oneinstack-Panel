#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
systemd_available || die_code CADDY_SYSTEMD_REQUIRED "Caddy uninstall requires systemd."
if [[ "${uninstall_data_policy}" == "delete" && "${uninstall_confirm_data_deletion}" != "true" ]]; then
  die_code CADDY_DATA_DELETE_CONFIRMATION_REQUIRED "Deleting Caddy data requires UNINSTALL_CONFIRM_DATA_DELETION=true."
fi
emit_progress 15 uninstall_stop "Stopping Caddy"
stop_managed_caddy
systemctl disable "${service_name}.service" >/dev/null 2>&1 || true
preserved="${state_dir}/removed/$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 -- "${preserved}"
[[ ! -d "${install_dir}" ]] || cp -a -- "${install_dir}" "${preserved}/install"
[[ ! -f "${unit_file}" ]] || cp -a -- "${unit_file}" "${preserved}/unit"
[[ ! -f "${installed_state_file}" ]] || cp -a -- "${installed_state_file}" "${preserved}/installed.json"
[[ ! -f "${install_parameters_file}" ]] || cp -a -- "${install_parameters_file}" "${preserved}/install-parameters"
[[ ! -f "${managed_path_acl_file}" ]] || cp -a -- "${managed_path_acl_file}" "${preserved}/managed-path-acl"
emit_progress 55 uninstall_files "Removing Caddy-owned binary, configuration, and service unit"
rm -rf -- "${install_dir}"
rm -f -- "${unit_file}" "${installed_state_file}" "${install_parameters_file}" "${rollback_pointer}"
systemctl daemon-reload
remove_managed_path_acl_entries "${managed_path_acl_file}"
rm -f -- "${managed_path_acl_file}"
if [[ "${uninstall_data_policy}" == "delete" ]]; then
  emit_progress 75 uninstall_data "Deleting /var/lib/caddy after explicit confirmation"
  rm -rf -- "${data_dir}"
else
  log "Caddy data remains at ${data_dir}; Panel vhosts remain at ${vhost_dir}."
fi
emit_progress 100 uninstall_complete "Caddy was uninstalled; preserved configuration snapshot: ${preserved}."
