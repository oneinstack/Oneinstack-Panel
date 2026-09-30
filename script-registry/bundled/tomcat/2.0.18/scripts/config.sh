#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"

require_root
load_persisted_parameters
validate_host
validate_scalar_inputs
config_file="${data_dir}/conf/server.xml"
setenv_file="${data_dir}/bin/setenv.sh"
operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"

connector_value() {
  local key="$1"
  sed -nE "s/.*<Connector[^>]*[[:space:]]${key}=\"([^\"]+)\".*/\1/p" "${config_file}" | head -n1
}

current_access_log() {
  if grep -q 'ONEINSTACK MANAGED ACCESS LOG' "${config_file}" && grep -q 'AccessLogValve' "${config_file}"; then
    printf 'true'
  else
    printf 'false'
  fi
}

emit_configuration() {
  local current_port current_bind current_threads current_queue current_timeout current_encoding current_xms current_xmx
  current_port="$(connector_value port)"
  current_bind="$(connector_value address)"
  current_threads="$(connector_value maxThreads)"
  current_queue="$(connector_value acceptCount)"
  current_timeout="$(connector_value connectionTimeout)"
  current_encoding="$(connector_value URIEncoding)"
  current_port="${current_port:-${tomcat_port}}"
  current_bind="${current_bind:-${bind_address}}"
  current_threads="${current_threads:-${max_threads}}"
  current_queue="${current_queue:-${accept_count}}"
  current_timeout="${current_timeout:-${connection_timeout_ms}}"
  current_encoding="${current_encoding:-${uri_encoding}}"
  if [[ -f "${setenv_file}" ]]; then
    current_xms="$(sed -nE 's/.*-Xms([0-9]+)m.*/\1/p' "${setenv_file}" | head -n1)"
    current_xmx="$(sed -nE 's/.*-Xmx([0-9]+)m.*/\1/p' "${setenv_file}" | head -n1)"
  fi
  current_xms="${current_xms:-${jvm_xms_mb}}"
  current_xmx="${current_xmx:-${jvm_xmx_mb}}"
  printf 'component=tomcat\nrevision=%s\napply_mode=restart\n' "$(config_revision)"
  printf 'httpPort=%s\nbindAddress=%s\njvmXmsMB=%s\njvmXmxMB=%s\nmaxThreads=%s\nacceptCount=%s\nconnectionTimeoutMs=%s\nuriEncoding=%s\naccessLogEnabled=%s\n' \
    "${current_port}" "${current_bind}" "${current_xms}" "${current_xmx}" "${current_threads}" "${current_queue}" "${current_timeout}" "${current_encoding}" "$(current_access_log)"
  printf 'runtime.port=%s\nruntime.bindAddress=%s\nruntime.installDir=%s\nruntime.dataDir=%s\nruntime.logDir=%s\nruntime.runUser=%s\nruntime.runGroup=%s\nruntime.configFile=%s\nruntime.serviceName=%s\nruntime.version=%s\nruntime.javaHome=%s\nruntime.jdkVersion=%s\n' \
    "${current_port}" "${current_bind}" "${install_dir}" "${data_dir}" "${log_dir}" "${run_user}" "${run_group}" "${config_file}" "${service_name}" "${software_version}" "${java_home}" "${jdk_version}"
}

if [[ "${operation}" == get ]]; then
  emit_configuration
  exit 0
fi
[[ "${operation}" == apply ]] || die "unsupported configuration operation"
[[ "${expected_revision}" =~ ^[0-9a-fA-F]{64}$ ]] || die "configuration revision is required"
[[ "$(config_revision)" == "${expected_revision,,}" ]] || {
  printf 'CONFIG_REVISION_MISMATCH: configuration changed since preview\n' >&2
  exit 75
}

target_port="${ONEINSTACK_CONFIG_HTTP_PORT:-$(connector_value port)}"
target_bind="${ONEINSTACK_CONFIG_BIND_ADDRESS:-$(connector_value address)}"
target_xms="${ONEINSTACK_CONFIG_JVM_XMS_MB:-${jvm_xms_mb}}"
target_xmx="${ONEINSTACK_CONFIG_JVM_XMX_MB:-${jvm_xmx_mb}}"
target_threads="${ONEINSTACK_CONFIG_MAX_THREADS:-${max_threads}}"
target_queue="${ONEINSTACK_CONFIG_ACCEPT_COUNT:-${accept_count}}"
target_timeout="${ONEINSTACK_CONFIG_CONNECTION_TIMEOUT_MS:-${connection_timeout_ms}}"
target_encoding="${ONEINSTACK_CONFIG_URI_ENCODING:-${uri_encoding}}"
target_access_log="${ONEINSTACK_CONFIG_ACCESS_LOG_ENABLED:-$(current_access_log)}"

tomcat_port="${target_port}"
bind_address="${target_bind}"
jvm_xms_mb="${target_xms}"
jvm_xmx_mb="${target_xmx}"
max_threads="${target_threads}"
accept_count="${target_queue}"
connection_timeout_ms="${target_timeout}"
uri_encoding="${target_encoding}"
access_log_enabled="${target_access_log}"
validate_scalar_inputs
port_is_available "${target_port}" "${target_bind}" || die "PORT_IN_USE: ${target_bind}:${target_port}"
ensure_managed_service_ownership
ensure_default_data_root_access
resolve_java_library_path
normalize_selinux_contexts "${install_dir}" "${java_home}" "${data_dir}"
validate_java_runtime_user_access

install -d -m 0750 "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
chmod 0700 "${backup_dir}"
cp -p -- "${config_file}" "${backup_dir}/server.xml"
cp -p -- "${setenv_file}" "${backup_dir}/setenv.sh"
printf '%s\n' "${expected_revision}" >"${backup_dir}/revision"

candidate_xml="$(mktemp "${config_file}.candidate.XXXXXX")"
candidate_setenv="$(mktemp "${setenv_file}.candidate.XXXXXX")"
cp -p -- "${config_file}" "${candidate_xml}"
write_connector_line "${candidate_xml}" "${target_port}" "${target_bind}" "${target_threads}" "${target_queue}" "${target_timeout}" "${target_encoding}"
write_access_log_setting "${candidate_xml}" "${target_access_log}"
chmod 0640 "${candidate_xml}"
chown root:"${run_group}" "${candidate_xml}"
cat >"${candidate_setenv}" <<EOF
#!/usr/bin/env bash
# ONEINSTACK MANAGED SETENV
export JAVA_HOME="${java_home}"
export LD_LIBRARY_PATH="${java_library_path}"
export CATALINA_HOME="${install_dir}"
export CATALINA_BASE="${data_dir}"
export CATALINA_OPTS="-Xms${target_xms}m -Xmx${target_xmx}m"
EOF
chmod 0750 "${candidate_setenv}"
chown root:"${run_group}" "${candidate_setenv}"
write_managed_service
systemctl daemon-reload

active=false
if systemctl is-active --quiet "${service_name}.service"; then active=true; fi
committed=false
restore_configuration() {
  set +e
  cp -p -- "${backup_dir}/server.xml" "${config_file}"
  cp -p -- "${backup_dir}/setenv.sh" "${setenv_file}"
  if [[ "${active}" == true ]]; then
    systemctl restart "${service_name}.service" >/dev/null 2>&1 || true
  fi
}
trap 'code=$?; if [[ "${committed}" == true ]]; then restore_configuration; fi; rm -f -- "${candidate_xml}" "${candidate_setenv}"; exit "${code}"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mv -f -- "${candidate_xml}" "${config_file}"
mv -f -- "${candidate_setenv}" "${setenv_file}"
committed=true
if [[ "${active}" == true ]]; then
  systemctl restart "${service_name}.service"
  sleep 2
  validate_runtime
fi
tomcat_port="${target_port}"
bind_address="${target_bind}"
jvm_xms_mb="${target_xms}"
jvm_xmx_mb="${target_xmx}"
max_threads="${target_threads}"
accept_count="${target_queue}"
connection_timeout_ms="${target_timeout}"
uri_encoding="${target_encoding}"
access_log_enabled="${target_access_log}"
persist_install_parameters
committed=false
trap - EXIT INT TERM
prune_config_backups
emit_configuration
