#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
runtime_version="$(component_version 2>/dev/null || true)"
service_unit="$(resolve_service_unit status 2>/dev/null || true)"
[[ -n "${service_unit}" ]] || service_unit="${service_name}.service"
load_state="$(systemctl_property_value "${service_unit}" LoadState 2>/dev/null || true)"
active_state="$(systemctl_property_value "${service_unit}" ActiveState 2>/dev/null || true)"
sub_state="$(systemctl_property_value "${service_unit}" SubState 2>/dev/null || true)"
unit_file_state="$(systemctl is-enabled "${service_unit}" 2>/dev/null || true)"
printf 'component=%s\nservice=%s\nload_state=%s\nactive_state=%s\nsub_state=%s\nunit_file_state=%s\nruntime_version=%s\ncan_reload=false\n' \
  "${component_id}" "${service_name}" "${load_state:-unknown}" "${active_state:-unknown}" \
  "${sub_state:-unknown}" "${unit_file_state:-unknown}" "${runtime_version}"
