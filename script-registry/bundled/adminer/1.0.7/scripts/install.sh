#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
transaction_started=false
on_error() {
  local rc=$?
  trap - ERR
  log_error "Adminer install/upgrade failed at line ${BASH_LINENO[0]:-unknown}; exit=${rc}."
  if [[ "${transaction_started}" == true ]]; then restore_transaction || true; fi
  exit "${rc}"
}
trap on_error ERR
require_root
load_saved_install_configuration
validate_common
validate_runtime_prerequisites
emit_progress 15 adminer.prerequisites.ready "Adminer package ${package_version}: managed PHP-FPM and ${web_component} prerequisites passed"
artifact="$(obtain_artifact)"
emit_progress 40 adminer.artifact.ready "Pinned Adminer artifact verified"
prepare_install_transaction
transaction_started=true
create_managed_installation "${artifact}"
ensure_php_fpm_route_access
emit_progress 70 adminer.route.ready "Transactional public route created"
verify_managed_installation
write_runtime_state
write_installed_state
cleanup_transaction
transaction_started=false
if [[ "${access_policy}" == public ]]; then
  log "SECURITY WARNING: Adminer is publicly reachable at ${public_path}; change the policy to local or allowlist for production."
fi
emit_progress 100 install_completed "Adminer 6.1.0 installed and HTTP verified"
