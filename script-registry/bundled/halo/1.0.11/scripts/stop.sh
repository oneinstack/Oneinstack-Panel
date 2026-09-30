#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
managed_installation_present || die "MANAGED_STATE_MISSING: Halo is not managed by OneinStack."
service_stop
