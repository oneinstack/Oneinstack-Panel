#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

runtime_version=""
command -v clamscan >/dev/null 2>&1 && runtime_version="$(clamscan --version 2>/dev/null | sed -nE 's/^ClamAV ([0-9]+(\.[0-9]+){1,3}).*/\1/p' | head -n1 || true)"
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_status
  exit 0
fi
if ! is_managed; then
  printf 'component=%s\nservice=%s\nload_state=not-found\nactive_state=inactive\nsub_state=dead\nunit_file_state=disabled\nruntime_version=%s\ncan_reload=false\n' "${component_id}" "${service_name}" "${runtime_version}"
  exit 0
fi
load_managed_state
read_state() {
  local property="$1" fallback="$2" value
  value="$(systemctl_property "${service_name}.service" "${property}")"
  [[ "${value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || value="${fallback}"
  printf '%s' "${value}"
}
load_state="$(read_state LoadState not-found)"
active_state="$(read_state ActiveState inactive)"
sub_state="$(read_state SubState dead)"
unit_file_state="$(read_state UnitFileState disabled)"
# stdout is the Panel's strict, cross-component status protocol. Detailed
# ClamAV health diagnostics belong to verify.sh and install failure details;
# emitting them here would make a healthy service card unparsable.
printf 'component=%s\nservice=%s\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "${component_id}" "${service_name}" "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "${runtime_version}"
