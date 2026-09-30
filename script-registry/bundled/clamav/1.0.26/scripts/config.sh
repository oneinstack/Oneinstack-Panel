#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

operation="${ONEINSTACK_CONFIG_OPERATION:-get}"
expected_revision="${ONEINSTACK_CONFIG_REVISION:-}"
backup_root="${state_dir}/config-backups"

revision() {
  sha256sum "${clamd_config}" "${freshclam_config}" "${settings_file}" | sha256sum | awk '{print $1}'
}

validate_boolean() {
  [[ "$1" == true || "$1" == false ]] || die "Invalid $2."
}

validate_mirror() {
  local mirror="$1"
  [[ -z "${mirror}" ]] && return 0
  [[ "${mirror}" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ ]] ||
    die 'Invalid databaseMirror; use a hostname without whitespace.'
}

validate_candidate() {
  local candidate_dir="$1" candidate_socket="$2" pid='' attempts=15 ready=false
  install -d -m 0750 -o "${runtime_user}" -g "${runtime_group}" -- "${runtime_dir}"
  chown -R "${runtime_user}:${runtime_group}" "${candidate_dir}"
  chmod 0750 "${candidate_dir}"
  chmod 0640 "${candidate_dir}/clamd.conf"
  chown root:root "${candidate_dir}/freshclam.conf"
  chmod 0600 "${candidate_dir}/freshclam.conf"
  validate_freshclam_config_file "${candidate_dir}/freshclam.conf"
  as_runtime_user "${clamd_binary}" --foreground --config-file="${candidate_dir}/clamd.conf" >"${candidate_dir}/clamd-validate.log" 2>&1 &
  pid=$!
  while ((attempts > 0)); do
    if [[ -S "${candidate_socket}" ]] && as_runtime_user "${clamdscan_binary}" --config-file="${candidate_dir}/clamd.conf" --ping 1 >/dev/null 2>&1; then
      ready=true
      break
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then break; fi
    sleep 1
    ((attempts--))
  done
  kill "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
  rm -f -- "${candidate_socket}"
  [[ "${ready}" == true ]] || {
    tail -n 50 "${candidate_dir}/clamd-validate.log" >&2 || true
    die 'CONFIG_VALIDATE_FAILED: candidate clamd configuration cannot start and answer a Unix Socket PING.'
  }
}

detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_config
  exit 0
fi
is_managed || die 'NOT_MANAGED: ClamAV managed configuration is unavailable.'
load_managed_state
[[ -x "${clamd_binary}" && -x "${clamdscan_binary}" && -x "${freshclam_binary}" ]] ||
  die 'RUNTIME_PROFILE_INVALID: ClamAV binaries are unavailable.'
[[ -f "${clamd_config}" && -f "${freshclam_config}" && -f "${settings_file}" ]] ||
  die 'CONFIG_UNAVAILABLE: managed configuration files are missing.'

if [[ "${operation}" == get ]]; then
  printf 'component=clamav\nrevision=%s\napply_mode=restart\n' "$(revision)"
  printf 'databaseUpdateMode=%s\nchecksPerDay=%s\ndatabaseMirror=%s\nmaxThreads=%s\nmaxQueue=%s\nmaxScanSizeMB=%s\nmaxFileSizeMB=%s\nmaxRecursion=%s\nmaxFiles=%s\n' \
    "$(current_update_mode)" "$(current_checks_per_day)" "$(current_database_mirror)" "$(current_max_threads)" "$(current_max_queue)" "$(current_max_scan_size)" "$(current_max_file_size)" "$(current_max_recursion)" "$(current_max_files)"
  verbose=false; [[ "$(current_log_verbose)" == yes ]] && verbose=true
  printf 'logVerbose=%s\n' "${verbose}"
  printf 'runtime.socketPath=%s\nruntime.dataDir=%s\nruntime.configFile=%s\nruntime.serviceName=%s\nruntime.runUser=%s\nruntime.runGroup=%s\nruntime.installSource=%s\nruntime.version=%s\nruntime.systemdState=%s\nruntime.databaseAgeHours=%s\n' \
    "${socket_path}" "${data_dir}" "${clamd_config}" "${service_name}" "${runtime_user}" "${runtime_group}" \
    "${install_mode}" "$(clamscan --version 2>/dev/null | awk '{print $2}' | head -n1)" "$(systemctl is-active "${service_name}.service" 2>/dev/null || true)" "$(database_age_hours_from_data)"
  exit 0
fi

[[ "${operation}" == apply ]] || die 'Unsupported configuration operation.'
require_root
ensure_cvd_certificates
configure_mandatory_access
[[ "${expected_revision}" =~ ^[0-9a-f]{64}$ ]] || die 'Invalid ClamAV configuration revision.'
[[ "$(revision)" == "${expected_revision}" ]] || { printf 'Configuration changed since preview; refresh and try again.\n' >&2; exit 75; }

update_mode="${ONEINSTACK_CONFIG_DATABASE_UPDATE_MODE:-}"
checks="${ONEINSTACK_CONFIG_CHECKS_PER_DAY:-}"
mirror="${ONEINSTACK_CONFIG_DATABASE_MIRROR:-}"
threads="${ONEINSTACK_CONFIG_MAX_THREADS:-}"
queue="${ONEINSTACK_CONFIG_MAX_QUEUE:-}"
scan_size="${ONEINSTACK_CONFIG_MAX_SCAN_SIZE_MB:-}"
file_size="${ONEINSTACK_CONFIG_MAX_FILE_SIZE_MB:-}"
recursion="${ONEINSTACK_CONFIG_MAX_RECURSION:-}"
files="${ONEINSTACK_CONFIG_MAX_FILES:-}"
verbose="${ONEINSTACK_CONFIG_LOG_VERBOSE:-}"

[[ "${update_mode}" == auto || "${update_mode}" == manual ]] || die 'Invalid databaseUpdateMode.'
[[ "${checks}" =~ ^[0-9]+$ && "${checks}" -ge 1 && "${checks}" -le 24 ]] || die 'checksPerDay must be 1-24.'
validate_mirror "${mirror}"
[[ -n "${mirror}" ]] || mirror=database.clamav.net
[[ "${threads}" =~ ^[0-9]+$ && "${threads}" -ge 1 && "${threads}" -le 64 ]] || die 'maxThreads must be 1-64.'
[[ "${queue}" =~ ^[0-9]+$ && "${queue}" -ge 1 && "${queue}" -le 512 ]] || die 'maxQueue must be 1-512.'
[[ "${scan_size}" =~ ^[0-9]+$ && "${scan_size}" -ge 1 && "${scan_size}" -le 4096 ]] || die 'maxScanSizeMB must be 1-4096.'
[[ "${file_size}" =~ ^[0-9]+$ && "${file_size}" -ge 1 && "${file_size}" -le 4096 && "${file_size}" -le "${scan_size}" ]] || die 'maxFileSizeMB must be 1-4096 and cannot exceed maxScanSizeMB.'
[[ "${recursion}" =~ ^[0-9]+$ && "${recursion}" -ge 1 && "${recursion}" -le 100 ]] || die 'maxRecursion must be 1-100.'
[[ "${files}" =~ ^[0-9]+$ && "${files}" -ge 1 && "${files}" -le 1000000 ]] || die 'maxFiles must be 1-1000000.'
validate_boolean "${verbose}" logVerbose
if [[ "${install_mode}" == offline && -n "${mirror}" && "${mirror}" != "$(current_database_mirror)" ]]; then
  die 'OFFLINE_NETWORK_FORBIDDEN: offline ClamAV installations cannot change databaseMirror.'
fi
if [[ "${install_mode}" == offline && "${update_mode}" == auto ]]; then
  die 'OFFLINE_NETWORK_FORBIDDEN: offline ClamAV installations cannot enable automatic FreshClam updates.'
fi
[[ "${verbose}" == true ]] && verbose=yes || verbose=no

candidate_dir="$(mktemp -d "${config_dir}/.config-candidate.XXXXXX")"
candidate_socket="${runtime_dir}/clamd-config-$$.sock"
cleanup_candidate() { rm -rf -- "${candidate_dir:-}"; }
trap cleanup_candidate EXIT
write_settings "${candidate_dir}/runtime-settings" "${update_mode}" "${checks}" "${mirror}" "${threads}" "${queue}" "${scan_size}" "${file_size}" "${recursion}" "${files}" "${verbose}"
write_clamd_config "${candidate_dir}/clamd.conf" "${candidate_socket}" "${threads}" "${queue}" "${scan_size}" "${file_size}" "${recursion}" "${files}" "${verbose}"
write_freshclam_config "${candidate_dir}/freshclam.conf" "${checks}" "${mirror}"
emit_progress 25 config_validate '正在启动临时 ClamAV Unix Socket 以验证候选配置'
validate_candidate "${candidate_dir}" "${candidate_socket}"
trap - EXIT

install -d -m 0700 -- "${backup_root}"
backup_dir="$(mktemp -d "${backup_root}/config-$(date -u +%Y%m%dT%H%M%SZ)-XXXXXX")"
cp -a -- "${clamd_config}" "${freshclam_config}" "${settings_file}" "${backup_dir}/"
was_active=false; systemctl is-active --quiet "${service_name}.service" && was_active=true
timer_was_active=false; systemctl is-active --quiet "${update_timer_name}" && timer_was_active=true
timer_was_enabled="$(systemctl is-enabled "${update_timer_name}" 2>/dev/null || true)"
committed=false
rollback_config() {
  local code="${1:-$?}"
  set +e
  if [[ "${committed}" == true ]]; then
    cp -a -- "${backup_dir}/clamd.conf" "${clamd_config}"
    cp -a -- "${backup_dir}/freshclam.conf" "${freshclam_config}"
    cp -a -- "${backup_dir}/runtime-settings" "${settings_file}"
    write_systemd_units "$(current_update_mode)" "$(current_checks_per_day)"
    if [[ "$(current_update_mode)" == auto ]]; then
      case "${timer_was_enabled}" in
        enabled|enabled-runtime|linked|linked-runtime|alias) systemctl enable "${update_timer_name}" 2>/dev/null || true ;;
        *) systemctl disable "${update_timer_name}" 2>/dev/null || true ;;
      esac
      if [[ "${timer_was_active}" == true ]]; then systemctl start "${update_timer_name}" 2>/dev/null || true; else systemctl stop "${update_timer_name}" 2>/dev/null || true; fi
    fi
    if [[ "${was_active}" == true ]]; then systemctl restart "${service_name}.service"; else systemctl stop "${service_name}.service"; fi
  fi
  rm -rf -- "${candidate_dir:-}"
  exit "${code}"
}
trap 'rollback_config $?' ERR
trap 'rollback_config 130' INT
trap 'rollback_config 143' TERM

emit_progress 60 config_publish '正在原子发布 ClamAV 配置并重启受管服务'
mv -f -- "${candidate_dir}/clamd.conf" "${clamd_config}"
mv -f -- "${candidate_dir}/freshclam.conf" "${freshclam_config}"
mv -f -- "${candidate_dir}/runtime-settings" "${settings_file}"
rm -rf -- "${candidate_dir}"
committed=true
configure_runtime_permissions
validate_freshclam_config
write_systemd_units "${update_mode}" "${checks}"
systemctl restart "${service_name}.service"
wait_for_healthy || { health_failure_detail; die 'SERVICE_NOT_READY: ClamAV did not become healthy after configuration apply.'; }
if [[ "${install_mode}" == center && "${update_mode}" == auto ]]; then
  systemctl enable --now "${update_timer_name}"
else
  systemctl disable --now "${update_timer_name}" 2>/dev/null || true
fi
if [[ "${was_active}" != true ]]; then
  systemctl stop "${service_name}.service"
fi
trap - ERR INT TERM
mapfile -t backups < <(find "${backup_root}" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' | sort -rn | cut -d' ' -f2-)
for ((index=20; index<${#backups[@]}; index++)); do rm -rf -- "${backups[index]}"; done
emit_progress 100 config_applied 'ClamAV 配置已验证并生效'
printf 'Configuration backup: %s\n' "$(basename -- "${backup_dir}")"
