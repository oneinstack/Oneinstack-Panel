#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
load_saved_install_configuration
log "Adminer package ${package_version} precheck started."
validate_common
log "Detected host ${detected_os_id} ${detected_os_release_version} (${detected_architecture})."
validate_runtime_prerequisites
if [[ "${access_policy}" == public ]]; then
  log "SECURITY WARNING: Adminer will be publicly reachable at ${public_path}; local or allowlist is recommended for production."
fi
emit_progress 100 precheck_completed "Adminer managed PHP, Web Server, route, and driver prerequisites passed"
