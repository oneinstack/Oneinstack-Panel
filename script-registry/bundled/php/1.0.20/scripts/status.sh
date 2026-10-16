#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
validate_inputs

read_systemd_state() {
  local property="$1" value
  systemd_available && managed_unit_matches || { printf '%s' "not_applicable"; return 0; }
  value="$(systemctl show "${service_name}.service" --property="${property}" --value 2>/dev/null || true)"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="unknown"
  printf '%s' "${value}"
}

runtime_version=""
if [[ -x "${install_dir}/bin/php" ]]; then
  runtime_version="$("${install_dir}/bin/php" -r 'echo PHP_VERSION;' 2>/dev/null || true)"
fi
recorded_version=""
[[ -f "${state_dir}/version" ]] && recorded_version="$(cat -- "${state_dir}/version")"
active_state="inactive"
sub_state="dead"
if service_is_active; then
  active_state="active"
  sub_state="running"
fi
version_state="unavailable"
if [[ -n "${runtime_version}" && -n "${recorded_version}" ]]; then
  if [[ "${runtime_version}" == "${recorded_version}" && "${runtime_version}" == "${software_version}" ]]; then
    version_state="matched"
  else
    version_state="drifted"
  fi
fi
ownership="unknown"
if managed_installation_present; then ownership="managed"; fi
socket_state="absent"
[[ -S "${socket_path}" ]] && socket_state="ready"

printf 'component=php\n'
printf 'service=%s\n' "${service_name}"
printf 'load_state=%s\n' "$(read_systemd_state LoadState)"
printf 'active_state=%s\n' "${active_state}"
printf 'sub_state=%s\n' "${sub_state}"
printf 'unit_file_state=%s\n' "$(read_systemd_state UnitFileState)"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'recorded_version=%s\n' "${recorded_version}"
printf 'version_state=%s\n' "${version_state}"
printf 'ownership=%s\n' "${ownership}"
printf 'socket_state=%s\n' "${socket_state}"
printf 'can_reload=true\n'
