#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

load_persisted_parameters
runtime_version="$(current_version || true)"
service_unit="${service_name}.service"
load_state="unknown"
active_state="unknown"
sub_state="unknown"
unit_file_state="unknown"

if systemd_available; then
  load_state="$(systemctl show "${service_unit}" --property=LoadState --value 2>/dev/null || true)"
  active_state="$(systemctl show "${service_unit}" --property=ActiveState --value 2>/dev/null || true)"
  sub_state="$(systemctl show "${service_unit}" --property=SubState --value 2>/dev/null || true)"
  unit_file_state="$(systemctl is-enabled "${service_unit}" 2>/dev/null || true)"
else
  if [[ -x "${binary}" && -r "${caddyfile}" ]]; then load_state="loaded"; else load_state="not-found"; fi
  if port_listening; then active_state="active"; sub_state="running"; else active_state="inactive"; sub_state="dead"; fi
fi

for state_name in load_state active_state sub_state unit_file_state; do
  state_value="${!state_name}"
  [[ "${state_value}" =~ ^[a-z][a-z0-9_-]{0,31}$ ]] || printf -v "${state_name}" '%s' unknown
done

printf 'component=caddy\n'
printf 'service=%s\n' "${service_name}"
printf 'load_state=%s\n' "${load_state}"
printf 'active_state=%s\n' "${active_state}"
printf 'sub_state=%s\n' "${sub_state}"
printf 'unit_file_state=%s\n' "${unit_file_state}"
printf 'runtime_version=%s\n' "${runtime_version}"
printf 'can_reload=true\n'
# An inactive service is a valid observation, not a probe execution failure.
exit 0
