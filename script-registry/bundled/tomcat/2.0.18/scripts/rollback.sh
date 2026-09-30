#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
if [[ ! -e "${rollback_root}/previous-install" && ! -L "${rollback_root}/previous-install" ]]; then
  printf 'Tomcat rollback skipped: no snapshot is available for this installation attempt.\n' >&2
  exit 0
fi
systemctl stop "${service_name}.service" >/dev/null 2>&1 || true
current_snapshot="${rollback_root}/current-install"
rm -rf -- "${current_snapshot}"
if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then mv -- "${install_dir}" "${current_snapshot}"; fi
mv -- "${rollback_root}/previous-install" "${install_dir}"
if [[ -f "${rollback_root}/previous-service" ]]; then cp -p -- "${rollback_root}/previous-service" "${service_unit}"; elif [[ -f "${rollback_root}/previous-service.absent" ]]; then rm -f -- "${service_unit}"; fi
if [[ -f "${rollback_root}/previous-installed.env" ]]; then cp -p -- "${rollback_root}/previous-installed.env" "${install_parameters_file}"; elif [[ -f "${rollback_root}/previous-installed.env.absent" ]]; then rm -f -- "${install_parameters_file}"; fi
if [[ -f "${rollback_root}/previous-installed.json" ]]; then cp -p -- "${rollback_root}/previous-installed.json" "${installed_state_file}"; elif [[ -f "${rollback_root}/previous-installed.json.absent" ]]; then rm -f -- "${installed_state_file}"; fi
load_persisted_parameters
if [[ -f "${rollback_root}/previous-java-ldconfig" ]]; then cp -p -- "${rollback_root}/previous-java-ldconfig" "${java_ldconfig_file}"; elif [[ -f "${rollback_root}/previous-java-ldconfig.absent" ]]; then rm -f -- "${java_ldconfig_file}"; fi
if [[ -f "${rollback_root}/previous-jdk.link" ]]; then
  rm -rf -- "${java_home}"
  ln -s -- "$(cat "${rollback_root}/previous-jdk.link")" "${java_home}"
elif [[ -f "${rollback_root}/previous-jdk.absent" ]]; then
  rm -rf -- "${java_home}"
fi
restore_install_configuration
ldconfig
systemctl daemon-reload
software_version="$(tomcat_version_from_binary)"
jdk_version_for_tomcat
ensure_default_data_root_access
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}" "${service_unit}" "${java_ldconfig_file}"
validate_java_runtime_user_access
systemctl start "${service_name}.service"
validate_runtime
printf 'Tomcat rollback completed.\n'
