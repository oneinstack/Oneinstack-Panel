#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
[[ -n "${service_name}" ]] || die "${component_name} does not expose a managed service."
prepare_web_service_unit
service_unit="$(resolve_service_unit restart 2>/dev/null || true)"
[[ -n "${service_unit}" ]] || service_unit="${service_name}.service"
systemctl restart "${service_unit}"
if [[ "restart" == "stop" ]]; then
  service_active && die "${component_name} did not become inactive."
else
  service_active || die "${service_unit} did not become active."
fi
emit_progress 100 service_action_completed "${component_name} service action completed"
