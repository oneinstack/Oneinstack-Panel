#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
[[ -n "${service_name}" ]] || die "${component_name} does not expose a managed service."
stop_service
emit_progress 100 service_action_completed "${component_name} service action completed"
