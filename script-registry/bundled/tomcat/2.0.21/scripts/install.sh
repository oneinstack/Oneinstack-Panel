#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

require_root
# The manager may call rollback after any install failure. A previous task's
# successful snapshot must never authorize rollback of this attempt.
rm -f -- "${rollback_attempt_file}"
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

# Stage the target runtime and validate the migrated configuration while the
# previous Tomcat is still running. A rejected candidate never enters rollback.
stage_root="$(mktemp -d /usr/local/.oneinstack-tomcat-stage.XXXXXX)"
# Configuration validation can need a new /data ACL before the installation
# snapshot exists. Keep it in the same transaction as the eventual install.
(umask 077; : >"${transaction_path_acl_file}")
chmod 0600 "${transaction_path_acl_file}"
cleanup_staging() {
  local code="$?"
  trap - EXIT INT TERM
  set +e
  restore_transaction_path_acl
  rm -rf -- "${stage_root}"
  exit "${code}"
}
trap cleanup_staging EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
chmod 0750 "${stage_root}"
chown root:"${run_group}" "${stage_root}"
safe_extract_archive "${tomcat_archive}" "${stage_root}/tomcat"
mapfile -t tomcat_roots < <(find "${stage_root}/tomcat" -mindepth 1 -maxdepth 1 -type d -print)
[[ "${#tomcat_roots[@]}" -eq 1 ]] || die "TOMCAT_ARTIFACT_MISSING: archive root is ambiguous"
target_home="${tomcat_roots[0]}"
chown -R root:root "${target_home}"
find "${stage_root}/tomcat" -type d -exec chmod 0755 {} +
find "${target_home}" -type f -exec chmod 0644 {} +
find "${target_home}/bin" -type f -exec chmod 0755 {} +
target_java_home="${java_home}"
if [[ -L "${target_java_home}" || ! -x "${target_java_home}/bin/java" ]]; then
  safe_extract_archive "${jdk_archive}" "${stage_root}/jdk"
  mapfile -t jdk_roots < <(find "${stage_root}/jdk" -mindepth 1 -maxdepth 1 -type d -print)
  [[ "${#jdk_roots[@]}" -eq 1 ]] || die "TOMCAT_ARTIFACT_MISSING: JDK archive root is ambiguous"
  target_java_home="${jdk_roots[0]}"
  find "${stage_root}/jdk" -type d -exec chmod 0755 {} +
  find "${target_java_home}" -type f -exec chmod a+r {} +
  find "${target_java_home}/bin" -type f -exec chmod a+x {} +
fi
source_conf="${data_dir}/conf"
if [[ -d "${source_conf}" ]]; then
  ensure_default_data_root_access
else
  source_conf="${target_home}/conf"
fi
candidate_xml="${stage_root}/server.xml"
prepare_server_configuration "${source_conf}/server.xml" "${candidate_xml}" "${target_home}/conf"
write_connector_line "${candidate_xml}" "${tomcat_port}" "${bind_address}" "${max_threads}" "${accept_count}" "${connection_timeout_ms}" "${uri_encoding}"
write_access_log_setting "${candidate_xml}" "${access_log_enabled}"
validate_server_configuration "${target_home}" "${target_java_home}" "${candidate_xml}" "${source_conf}"

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
  if [[ "${was_enabled}" == false ]]; then systemctl disable "${service_name}.service" >/dev/null 2>&1 || true; fi
  if [[ "${was_active}" == true ]]; then systemctl start "${service_name}.service" >/dev/null 2>&1 || true; fi
  rm -f -- "${rollback_attempt_file}"
  rm -rf -- "${stage_root:-}"
  printf 'TOMCAT_ERROR: installation rolled back\n' >&2
  exit "${code}"
}
rm -rf -- "${rollback_root}/previous-install"
rm -rf -- "${data_layout_backup}"
rm -rf -- "${install_config_snapshot}"
rm -f -- "${rollback_root}/previous-install.absent" "${rollback_root}/previous-jdk.absent" "${rollback_root}/previous-jdk.link" "${rollback_root}/previous-jdk.path" \
  "${rollback_root}/previous-service" "${rollback_root}/previous-service.absent" \
  "${rollback_root}/previous-installed.env" "${rollback_root}/previous-installed.env.absent" \
  "${rollback_root}/previous-installed.json" "${rollback_root}/previous-installed.json.absent" \
  "${rollback_root}/previous-active" "${rollback_root}/previous-enabled" \
  "${rollback_root}/previous-java-ldconfig" "${rollback_root}/previous-java-ldconfig.absent"
if [[ -f "${service_unit}" ]]; then cp -p -- "${service_unit}" "${rollback_root}/previous-service"; else touch "${rollback_root}/previous-service.absent"; fi
if [[ -f "${install_parameters_file}" ]]; then cp -p -- "${install_parameters_file}" "${rollback_root}/previous-installed.env"; else touch "${rollback_root}/previous-installed.env.absent"; fi
if [[ -f "${installed_state_file}" ]]; then cp -p -- "${installed_state_file}" "${rollback_root}/previous-installed.json"; else touch "${rollback_root}/previous-installed.json.absent"; fi
if [[ -f "${java_ldconfig_file}" ]]; then cp -p -- "${java_ldconfig_file}" "${rollback_root}/previous-java-ldconfig"; else touch "${rollback_root}/previous-java-ldconfig.absent"; fi
if [[ "${was_active}" == true ]]; then touch "${rollback_root}/previous-active"; fi
if [[ "${was_enabled}" == true ]]; then touch "${rollback_root}/previous-enabled"; fi
printf '%s\n' "${java_home}" >"${rollback_root}/previous-jdk.path"
printf '%s\n' "${ONEINSTACK_TASK_ID:-manual}" >"${rollback_attempt_file}"
chmod 0600 "${rollback_root}/previous-jdk.path" "${rollback_attempt_file}"
trap rollback_on_error EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
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

mv -- "${target_home}" "${install_dir}"

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
  install -d -m 0755 "$(dirname "${jdk_target}")"
  rm -rf -- "${jdk_target}"
  mv -- "${target_java_home}" "${jdk_target}"
  chown -R root:root "${jdk_target}"
  find "${jdk_target}" -type d -exec chmod 0755 {} +
  find "${jdk_target}/bin" -type f -exec chmod 0755 {} +
fi
normalize_java_home_permissions
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}"
validate_java_runtime_user_access

install -m 0640 -- "${candidate_xml}" "${data_dir}/conf/server.xml"
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
