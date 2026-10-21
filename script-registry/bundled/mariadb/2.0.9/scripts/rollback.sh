#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs; restore_rollback
rm -f -- "${state_dir}/pending-version" "${state_dir}/pending-patch-version"
echo "MariaDB rollback completed. Existing database data was not rewritten."
