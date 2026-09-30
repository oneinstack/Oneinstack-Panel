#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
verify_installed || die "${component_name} installation verification failed."
write_state
emit_progress 100 verify_completed "${component_name} verification completed"
