#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
if [[ ! -r "${rollback_attempt_file}" ]] ||
  [[ "$(cat "${rollback_attempt_file}")" != "${ONEINSTACK_TASK_ID:-manual}" ]]; then
  printf 'Tomcat rollback skipped: no unconsumed snapshot belongs to this installation attempt.\n' >&2
  exit 0
fi
if [[ ! -e "${rollback_root}/previous-install" && ! -L "${rollback_root}/previous-install" && ! -f "${rollback_root}/previous-install.absent" ]]; then
  printf 'Tomcat rollback skipped: no snapshot is available for this installation attempt.\n' >&2
  exit 0
fi
rollback_java_home="$(cat "${rollback_root}/previous-jdk.path")"
case "${rollback_java_home}" in
  /usr/lib/jvm/oneinstack-tomcat-jdk-8|/usr/lib/jvm/oneinstack-tomcat-jdk-17) ;;
  *) die "TOMCAT_ROLLBACK_INVALID: target JDK snapshot path is invalid" ;;
esac
systemctl stop "${service_name}.service" >/dev/null 2>&1 || true
current_snapshot="${rollback_root}/current-install"
rm -rf -- "${current_snapshot}"
if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then mv -- "${install_dir}" "${current_snapshot}"; fi
if [[ -e "${rollback_root}/previous-install" || -L "${rollback_root}/previous-install" ]]; then
  mv -- "${rollback_root}/previous-install" "${install_dir}"
fi
if [[ -f "${rollback_root}/previous-service" ]]; then cp -p -- "${rollback_root}/previous-service" "${service_unit}"; elif [[ -f "${rollback_root}/previous-service.absent" ]]; then rm -f -- "${service_unit}"; fi
if [[ -f "${rollback_root}/previous-installed.env" ]]; then cp -p -- "${rollback_root}/previous-installed.env" "${install_parameters_file}"; elif [[ -f "${rollback_root}/previous-installed.env.absent" ]]; then rm -f -- "${install_parameters_file}"; fi
if [[ -f "${rollback_root}/previous-installed.json" ]]; then cp -p -- "${rollback_root}/previous-installed.json" "${installed_state_file}"; elif [[ -f "${rollback_root}/previous-installed.json.absent" ]]; then rm -f -- "${installed_state_file}"; fi
if [[ -f "${rollback_root}/previous-java-ldconfig" ]]; then cp -p -- "${rollback_root}/previous-java-ldconfig" "${java_ldconfig_file}"; elif [[ -f "${rollback_root}/previous-java-ldconfig.absent" ]]; then rm -f -- "${java_ldconfig_file}"; fi
if [[ -f "${rollback_root}/previous-jdk.link" ]]; then
  rm -rf -- "${rollback_java_home}"
  ln -s -- "$(cat "${rollback_root}/previous-jdk.link")" "${rollback_java_home}"
elif [[ -f "${rollback_root}/previous-jdk.absent" ]]; then
  rm -rf -- "${rollback_java_home}"
fi
load_persisted_parameters
restore_install_configuration
restore_managed_data_layout
restore_transaction_path_acl
ldconfig
systemctl daemon-reload
if [[ -f "${rollback_root}/previous-enabled" ]]; then
  systemctl enable "${service_name}.service" >/dev/null 2>&1
else
  systemctl disable "${service_name}.service" >/dev/null 2>&1 || true
fi
if [[ -f "${rollback_root}/previous-install.absent" ]]; then
  rm -f -- "${rollback_attempt_file}"
  printf 'Tomcat rollback completed; no previous installation existed.\n'
  exit 0
fi
software_version="$(tomcat_version_from_binary)"
jdk_version_for_tomcat
ensure_default_data_root_access
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}" "${service_unit}" "${java_ldconfig_file}"
validate_java_runtime_user_access
if [[ -f "${rollback_root}/previous-active" ]]; then
  systemctl start "${service_name}.service"
  validate_runtime
fi
rm -f -- "${rollback_attempt_file}"
printf 'Tomcat rollback completed.\n'
