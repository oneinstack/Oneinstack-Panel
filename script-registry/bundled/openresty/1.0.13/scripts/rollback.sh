#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host
if [[ -d "${rollback_dir}" ]]; then
  restore_rollback
else
  echo "No OpenResty rollback point was created before the installation failed."
fi
rm -f -- "${state_dir}/pending-version"
echo "OpenResty rollback completed."
