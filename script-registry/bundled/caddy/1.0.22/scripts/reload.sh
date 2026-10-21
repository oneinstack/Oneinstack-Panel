#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_persisted_parameters
validate_inputs
active_unit "${service_name}.service" || die_code CADDY_SERVICE_INACTIVE "Caddy service is not active."
validate_caddy_config "${caddyfile}" >/dev/null
reload_managed_caddy
active_unit "${service_name}.service" || die_code CADDY_RELOAD_FAILED "Caddy service became inactive after reload."
port_listening || die_code CADDY_RELOAD_FAILED "Caddy listener disappeared after reload."
emit_progress 100 service_reloaded "Caddy service reloaded."
