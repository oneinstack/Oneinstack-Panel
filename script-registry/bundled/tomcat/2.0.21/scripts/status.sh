#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

load_persisted_parameters

read_service_state() {
  local property="$1" fallback="$2" output value
  output="$(systemctl show "${service_name}.service" --property="${property}" 2>/dev/null || true)"
  if [[ "${output}" == "${property}="* ]]; then
    value="${output#*=}"
  fi
  [[ "${value:-}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="${fallback}"
  printf '%s' "${value}"
}

load_state=not-found
active_state=inactive
sub_state=dead
unit_file_state=disabled
if command_exists systemctl; then
  load_state="$(read_service_state LoadState not-found)"
  active_state="$(read_service_state ActiveState inactive)"
  sub_state="$(read_service_state SubState dead)"
  unit_file_state="$(read_service_state UnitFileState disabled)"
fi
runtime_version="$(tomcat_version_from_binary 2>/dev/null || true)"

printf 'component=%s\nservice=%s\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "${component_id}" "${service_name}" "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "${runtime_version}"
