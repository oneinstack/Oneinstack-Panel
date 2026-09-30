#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
load_state=not-found; active_state=inactive; sub_state=dead; unit_file_state=disabled
if command -v systemctl >/dev/null 2>&1; then
  read_state() {
    local property="$1" fallback="$2" value
    if [[ "${property}" == "UnitFileState" ]]; then
      value="$(systemctl is-enabled docker.service 2>/dev/null || true)"
    else
      value="$(systemctl show docker.service --property="${property}" 2>/dev/null |
        sed -n "s/^${property}=//p" || true)"
    fi
    [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="${fallback}"
    printf '%s' "${value}"
  }
  load_state="$(read_state LoadState not-found)"
  active_state="$(read_state ActiveState inactive)"
  sub_state="$(read_state SubState dead)"
  unit_file_state="$(read_state UnitFileState disabled)"
fi
runtime_version="$(docker_version || true)"
printf 'component=docker\nservice=docker\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "${runtime_version}"
