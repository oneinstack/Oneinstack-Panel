#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
if [[ -r "${runtime_state}" ]]; then load_runtime_state; fi
validate_public_path "${public_path}"
if managed_component_present php; then detect_managed_php || true; fi
if managed_component_present nginx || managed_component_present openresty || managed_component_present tengine || managed_component_present apache || managed_component_present caddy; then
  detect_managed_web_server || true
fi
restore_transaction
printf 'Adminer rollback completed.\n'
