#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; check_host; restore_rollback
rm -f -- "${state_dir}/pending-version"
echo "Nginx rollback completed."
