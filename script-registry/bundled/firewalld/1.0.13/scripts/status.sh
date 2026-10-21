#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

validate_inputs
check_host
if [[ "${ONEINSTACK_STATUS_SCOPE:-service}" == "installation" ]]; then
  probe_installation
  exit 0
fi
active_state="inactive"
sub_state="dead"
unit_file_state="not-found"
load_state="not-found"
if systemd_available; then
  load_state="$(systemctl show firewalld.service --property=LoadState --value 2>/dev/null || true)"
  active_state="$(systemctl show firewalld.service --property=ActiveState --value 2>/dev/null || true)"
  sub_state="$(systemctl show firewalld.service --property=SubState --value 2>/dev/null || true)"
  unit_file_state="$(systemctl is-enabled firewalld.service 2>/dev/null || true)"
elif service_active; then
  load_state="not-applicable"
  active_state="active"
  sub_state="running"
  unit_file_state="not-applicable"
else
  load_state="not-applicable"
fi
printf 'component=firewalld\n'
printf 'service=firewalld\n'
printf 'load_state=%s\n' "${load_state:-unknown}"
printf 'active_state=%s\n' "${active_state:-unknown}"
printf 'sub_state=%s\n' "${sub_state:-unknown}"
printf 'unit_file_state=%s\n' "${unit_file_state:-unknown}"
actual_version="$(runtime_version || true)"
recorded_version="$(cat "${state_dir}/runtime-version" 2>/dev/null || true)"
printf 'runtime_version=%s\n' "${actual_version}"
printf 'recorded_version=%s\n' "${recorded_version}"
version_state="unverified"
if firewalld_package_installed && [[ -n "${actual_version}" ]] && firewalld_configuration_valid &&
  [[ "${load_state}" != "not-found" ]]; then
  version_state="verified"
fi
printf 'version_state=%s\n' "${version_state}"
if [[ -f "${installed_marker}" ]] && firewalld_package_installed; then printf 'ownership=oneinstack\n'; else printf 'ownership=external\n'; fi
can_reload=false
if command -v firewall-cmd >/dev/null 2>&1 && service_active; then can_reload=true; fi
printf 'can_reload=%s\n' "${can_reload}"
