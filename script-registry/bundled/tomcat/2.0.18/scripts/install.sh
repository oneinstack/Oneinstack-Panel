#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

require_root
validate_inputs
validate_install_port
install_dependencies
obtain_artifact tomcat
tomcat_archive="${artifact_path}"
obtain_artifact "jdk${jdk_version}"
jdk_archive="${artifact_path}"
create_runtime_account
install -d -m 0750 "${state_dir}" "${rollback_root}"
validate_install_port

was_active=false
was_enabled=false
if systemctl is-active --quiet "${service_name}.service" 2>/dev/null; then was_active=true; fi
if systemctl is-enabled --quiet "${service_name}.service" 2>/dev/null; then was_enabled=true; fi

rollback_on_error() {
  local code="$?"
  set +e
  systemctl stop "${service_name}.service" >/dev/null 2>&1 || true
  if [[ -e "${rollback_root}/previous-install" || -L "${rollback_root}/previous-install" ]]; then
    rm -rf -- "${install_dir}"
    mv -- "${rollback_root}/previous-install" "${install_dir}"
  elif [[ -f "${rollback_root}/previous-install.absent" ]]; then
    rm -rf -- "${install_dir}"
  fi
  if [[ -f "${rollback_root}/previous-jdk.link" ]]; then
    rm -rf -- "${java_home}"
    ln -s -- "$(cat "${rollback_root}/previous-jdk.link")" "${java_home}"
  elif [[ -f "${rollback_root}/previous-jdk.absent" ]]; then
    rm -rf -- "${java_home}"
  fi
  if [[ -f "${rollback_root}/previous-service" ]]; then cp -p -- "${rollback_root}/previous-service" "${service_unit}"; elif [[ -f "${rollback_root}/previous-service.absent" ]]; then rm -f -- "${service_unit}"; fi
  if [[ -f "${rollback_root}/previous-installed.env" ]]; then cp -p -- "${rollback_root}/previous-installed.env" "${install_parameters_file}"; elif [[ -f "${rollback_root}/previous-installed.env.absent" ]]; then rm -f -- "${install_parameters_file}"; fi
  if [[ -f "${rollback_root}/previous-installed.json" ]]; then cp -p -- "${rollback_root}/previous-installed.json" "${installed_state_file}"; elif [[ -f "${rollback_root}/previous-installed.json.absent" ]]; then rm -f -- "${installed_state_file}"; fi
  if [[ -f "${rollback_root}/previous-java-ldconfig" ]]; then cp -p -- "${rollback_root}/previous-java-ldconfig" "${java_ldconfig_file}"; elif [[ -f "${rollback_root}/previous-java-ldconfig.absent" ]]; then rm -f -- "${java_ldconfig_file}"; fi
  restore_install_configuration
  restore_managed_data_layout
  restore_transaction_path_acl
  ldconfig >/dev/null 2>&1 || true
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [[ "${was_enabled}" == true ]]; then systemctl enable "${service_name}.service" >/dev/null 2>&1 || true; fi
  if [[ "${was_active}" == true ]]; then systemctl start "${service_name}.service" >/dev/null 2>&1 || true; fi
  rm -rf -- "${stage_root:-}"
  printf 'TOMCAT_ERROR: installation rolled back\n' >&2
  exit "${code}"
}
trap rollback_on_error EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

rm -rf -- "${rollback_root}/previous-install"
rm -rf -- "${data_layout_backup}"
rm -rf -- "${install_config_snapshot}"
rm -f -- "${transaction_path_acl_file}" "${rollback_root}/previous-install.absent" "${rollback_root}/previous-jdk.absent" "${rollback_root}/previous-jdk.link" \
  "${rollback_root}/previous-service" "${rollback_root}/previous-service.absent" \
  "${rollback_root}/previous-installed.env" "${rollback_root}/previous-installed.env.absent" \
  "${rollback_root}/previous-installed.json" "${rollback_root}/previous-installed.json.absent" \
  "${rollback_root}/previous-java-ldconfig" "${rollback_root}/previous-java-ldconfig.absent"
touch "${transaction_path_acl_file}"
if [[ -f "${service_unit}" ]]; then cp -p -- "${service_unit}" "${rollback_root}/previous-service"; else touch "${rollback_root}/previous-service.absent"; fi
if [[ -f "${install_parameters_file}" ]]; then cp -p -- "${install_parameters_file}" "${rollback_root}/previous-installed.env"; else touch "${rollback_root}/previous-installed.env.absent"; fi
if [[ -f "${installed_state_file}" ]]; then cp -p -- "${installed_state_file}" "${rollback_root}/previous-installed.json"; else touch "${rollback_root}/previous-installed.json.absent"; fi
if [[ -f "${java_ldconfig_file}" ]]; then cp -p -- "${java_ldconfig_file}" "${rollback_root}/previous-java-ldconfig"; else touch "${rollback_root}/previous-java-ldconfig.absent"; fi
if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then
  systemctl stop "${service_name}.service" >/dev/null 2>&1 || true
  mv -- "${install_dir}" "${rollback_root}/previous-install"
else
  touch "${rollback_root}/previous-install.absent"
fi
if [[ -L "${java_home}" ]]; then
  readlink -- "${java_home}" >"${rollback_root}/previous-jdk.link"
  rm -- "${java_home}"
elif [[ ! -e "${java_home}" ]]; then
  touch "${rollback_root}/previous-jdk.absent"
fi

stage_root="$(mktemp -d "${state_dir}/stage.XXXXXX")"
safe_extract_archive "${tomcat_archive}" "${stage_root}/tomcat"
mapfile -t tomcat_roots < <(find "${stage_root}/tomcat" -mindepth 1 -maxdepth 1 -type d -print)
[[ "${#tomcat_roots[@]}" -eq 1 ]] || die "TOMCAT_ARTIFACT_MISSING: archive root is ambiguous"
mv -- "${tomcat_roots[0]}" "${install_dir}"
chown -R root:root "${install_dir}"
find "${install_dir}" -type f -exec chmod 0644 {} +
find "${install_dir}/bin" -type f -exec chmod 0755 {} +
find "${install_dir}" -type d -exec chmod 0755 {} +

prepare_managed_data_layout
if [[ ! -d "${data_dir}/conf" ]]; then
  install -d -m 0750 "${data_dir}"
  cp -a -- "${install_dir}/conf" "${data_dir}/conf"
fi
if [[ ! -d "${data_dir}/webapps" ]]; then cp -a -- "${install_dir}/webapps" "${data_dir}/webapps"; fi
ensure_runtime_dirs
snapshot_install_configuration
ensure_default_data_root_access

jdk_target="${java_home}"
if [[ ! -x "${jdk_target}/bin/java" ]]; then
  jdk_stage="$(mktemp -d "${state_dir}/jdk.XXXXXX")"
  safe_extract_archive "${jdk_archive}" "${jdk_stage}"
  mapfile -t jdk_roots < <(find "${jdk_stage}" -mindepth 1 -maxdepth 1 -type d -print)
  [[ "${#jdk_roots[@]}" -eq 1 ]] || die "TOMCAT_ARTIFACT_MISSING: JDK archive root is ambiguous"
  install -d -m 0755 "$(dirname "${jdk_target}")"
  rm -rf -- "${jdk_target}"
  mv -- "${jdk_roots[0]}" "${jdk_target}"
  chown -R root:root "${jdk_target}"
  find "${jdk_target}" -type d -exec chmod 0755 {} +
  find "${jdk_target}/bin" -type f -exec chmod 0755 {} +
  rm -rf -- "${jdk_stage}"
fi
normalize_java_home_permissions
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}"
validate_java_runtime_user_access

write_connector_line "${data_dir}/conf/server.xml" "${tomcat_port}" "${bind_address}" "${max_threads}" "${accept_count}" "${connection_timeout_ms}" "${uri_encoding}"
write_access_log_setting "${data_dir}/conf/server.xml" "${access_log_enabled}"
write_setenv
chown -R "${run_user}:${run_group}" "${data_dir}"
write_managed_service
systemctl daemon-reload
systemctl enable --now "${service_name}.service"
sleep 2
validate_runtime
persist_install_parameters
commit_managed_data_layout
commit_transaction_path_acl
rm -rf -- "${stage_root}"
trap - EXIT INT TERM
printf 'Tomcat %s installed with Temurin JDK %s.\n' "${software_version}" "${jdk_version}"
