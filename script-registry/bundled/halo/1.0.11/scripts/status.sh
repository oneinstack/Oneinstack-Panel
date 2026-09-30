#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

read_property() {
  local property="$1" fallback="$2" value
  value="$(systemctl show "${service_name}.service" --property="${property}" --no-pager 2>/dev/null | sed -n "s/^${property}=//p" | head -n1 || true)"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="${fallback}"
  printf '%s' "${value}"
}
load_state=not-found; active_state=inactive; sub_state=dead; unit_file_state=disabled
if command -v systemctl >/dev/null 2>&1; then
  load_state="$(read_property LoadState not-found)"
  active_state="$(read_property ActiveState inactive)"
  sub_state="$(read_property SubState dead)"
  unit_file_state="$(read_property UnitFileState disabled)"
fi
runtime_version="$(cat "${state_dir}/version" 2>/dev/null || true)"
[[ "${runtime_version}" =~ ^[0-9A-Za-z][0-9A-Za-z._:+-]{0,63}$ ]] || runtime_version=""
printf 'component=halo\nservice=halo\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\n' \
  "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "${runtime_version}"
printf 'can_reload=false\n'
