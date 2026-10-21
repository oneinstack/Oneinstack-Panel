#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
if [[ -f "${state_dir}/migration/new-install" || -f "${state_dir}/migration/external-detected" ]]; then
  restore_existing_component
else
  export PRESERVE_DATA=true
  exec "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/uninstall.sh"
fi
