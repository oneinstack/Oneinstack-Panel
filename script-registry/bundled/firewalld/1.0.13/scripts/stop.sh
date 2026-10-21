#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
ensure_service_stopped
if service_active; then die "SERVICE_STOP_FAILED" "firewalld remains active after stop."; fi
emit_progress 100 service.stopped "firewalld stopped; its boot-time enablement state was preserved"
