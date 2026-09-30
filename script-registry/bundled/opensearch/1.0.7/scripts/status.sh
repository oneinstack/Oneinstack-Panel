#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

load_install_parameters
read_state() {
  local property="$1" fallback="$2" value
  value="$(systemctl show "${service_name}.service" --property="${property}" 2>/dev/null || true)"
  value="${value#*=}"; [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="${fallback}"
  printf '%s' "${value}"
}
load_state=not-found; active_state=inactive; sub_state=dead; unit_file_state=disabled
if command -v systemctl >/dev/null 2>&1; then
  load_state="$(read_state LoadState not-found)"; active_state="$(read_state ActiveState inactive)"
  sub_state="$(read_state SubState dead)"; unit_file_state="$(read_state UnitFileState disabled)"
fi
printf 'component=%s\nservice=%s\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "${component_id}" "${service_name}" "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "$(actual_version)"
