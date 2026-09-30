#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
if [[ -d "${migration_dir}" ]]; then
  restore_existing
else
  service_stop_disable
  remove_managed_integration
  if [[ -f "${installed_marker}" ]]; then
    remove_package
    rm -f -- "${installed_marker}"
  fi
  rm -f -- "${state_dir}/installed.json"
  rmdir "${state_dir}" 2>/dev/null || true
fi
echo "Fail2ban installation rollback completed."
