#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_persisted_parameters
validate_inputs
precheck_runtime_conflicts
validate_caddy_config "${caddyfile}" >/dev/null
systemctl restart "${service_name}.service"
verify_runtime
emit_progress 100 service_restarted "Caddy service restarted."
