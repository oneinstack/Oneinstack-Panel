#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

validate_inputs
load_state="not-found"; active_state="inactive"; sub_state="dead"; unit_file_state="disabled"
service_unit="${service_name}.service"
systemctl_property() {
  local property="$1" output
  output="$(systemctl show -p "${property}" "${service_unit}" 2>/dev/null || true)"
  case "${output}" in
    "${property}="*) printf '%s\n' "${output#*=}" ;;
  esac
}
if command -v systemctl >/dev/null 2>&1; then
  load_state="$(systemctl_property LoadState)"
  active_state="$(systemctl_property ActiveState)"
  sub_state="$(systemctl_property SubState)"
  unit_file_state="$(systemctl is-enabled "${service_unit}" 2>/dev/null || true)"
  [[ -n "${load_state}" ]] || load_state="not-found"
  [[ -n "${active_state}" ]] || active_state="inactive"
  [[ -n "${sub_state}" ]] || sub_state="dead"
  [[ -n "${unit_file_state}" ]] || unit_file_state="disabled"
fi
runtime_version=""
if command -v fail2ban-client >/dev/null 2>&1; then
  runtime_version="$(fail2ban-client --version 2>&1 | sed -n 's/^Fail2Ban v//p' | head -n1)"
fi
printf 'component=fail2ban\nservice=fail2ban\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=true\n' \
  "${load_state}" "${active_state}" "${sub_state}" "${unit_file_state}" "${runtime_version}"
