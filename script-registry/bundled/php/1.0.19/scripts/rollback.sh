#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
if ! rollback_belongs_to_current_operation; then
  # Installation can fail before prepare_rollback creates a snapshot (for
  # example while downloading the source). In that case no managed state was
  # changed and invoking rollback must remain a successful no-op.
  echo "component=php"
  echo "rollback=not_required"
  exit 0
fi
if [[ ! -d "${rollback_dir}/install" ]]; then
  # A failure after stopping the old service but before moving its files
  # still belongs to this operation and needs the old service restarted.
  if [[ -f "${rollback_dir}/was-active" ]]; then
    start_managed_service
    wait_for_socket || die_code ROLLBACK_FAILED "The previous PHP-FPM service did not become ready after rollback"
  fi
  finish_install_operation
  echo "component=php"
  echo "rollback=not_required"
  exit 0
fi
restore_rollback
rm -f -- "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
rm -f -- "${state_dir}/pending-runtime-params"
finish_install_operation
echo "component=php"
echo "rollback=completed"
