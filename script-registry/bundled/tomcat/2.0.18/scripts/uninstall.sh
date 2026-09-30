#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
load_persisted_parameters
ensure_managed_service_ownership
systemctl disable --now "${service_name}.service" >/dev/null 2>&1 || true
rm -f -- "${service_unit}"
systemctl daemon-reload
rm -rf -- "${install_dir}"
if [[ "${UNINSTALL_CONFIRM_DATA_DELETION:-false}" == true ]]; then
  rm -rf -- "${data_dir}"
else
  printf 'Tomcat data preserved at %s. Set UNINSTALL_CONFIRM_DATA_DELETION=true to delete it.\n' "${data_dir}"
fi
rm -rf -- "${java_home}"
if [[ -f "${java_ldconfig_file}" ]]; then
  rm -f -- "${java_ldconfig_file}"
  ldconfig
fi
remove_all_managed_path_acl
rm -f -- "${install_parameters_file}" "${installed_state_file}"
printf 'Tomcat program and managed JDK removed.\n'
