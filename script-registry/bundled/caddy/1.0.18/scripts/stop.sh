#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
systemd_available || die_code CADDY_SYSTEMD_REQUIRED "Caddy stop requires systemd."
if active_unit "${service_name}.service"; then systemctl stop "${service_name}.service"; fi
active_unit "${service_name}.service" && die_code CADDY_STOP_FAILED "Caddy service remained active."
emit_progress 100 service_stopped "Caddy service stopped."
