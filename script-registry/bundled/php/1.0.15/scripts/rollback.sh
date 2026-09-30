#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
if [[ ! -d "${rollback_dir}/install" ]]; then
  # Installation can fail before prepare_rollback creates a snapshot (for
  # example while downloading the source). In that case no managed state was
  # changed and invoking rollback must remain a successful no-op.
  echo "component=php"
  echo "rollback=not_required"
  exit 0
fi
restore_rollback
rm -f -- "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
rm -f -- "${state_dir}/pending-runtime-params"
echo "component=php"
echo "rollback=completed"
