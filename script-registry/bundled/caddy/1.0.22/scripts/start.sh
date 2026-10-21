#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_persisted_parameters
validate_inputs
[[ -x "${binary}" && -r "${caddyfile}" ]] || die_code CADDY_NOT_INSTALLED "Caddy is not installed."
precheck_runtime_conflicts
validate_caddy_config "${caddyfile}" >/dev/null
systemctl start "${service_name}.service"
verify_runtime
emit_progress 100 service_started "Caddy service started."
