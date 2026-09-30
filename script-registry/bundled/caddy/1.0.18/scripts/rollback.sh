#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
systemd_available || die_code CADDY_SYSTEMD_REQUIRED "Caddy rollback requires systemd."
emit_progress 15 rollback_prepare "Restoring the latest Caddy installation snapshot"
restore_rollback_snapshot || die_code CADDY_ROLLBACK_FAILED "Caddy rollback restoration failed."
if [[ -r "${install_parameters_file}" ]]; then load_persisted_parameters; fi
if active_unit "${service_name}.service"; then
  validate_caddy_config "${caddyfile}" >/dev/null
  port_listening || die_code CADDY_ROLLBACK_FAILED "Restored Caddy service has no listener on ${caddy_port}."
fi
emit_progress 100 rollback_complete "Caddy rollback completed."
