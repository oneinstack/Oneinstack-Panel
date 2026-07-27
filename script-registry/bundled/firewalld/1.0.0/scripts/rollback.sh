#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
if [[ -f "${installed_marker}" ]]; then
  ensure_firewalld_stopped
  remove_firewalld_package
  rm -f -- "${installed_marker}"
fi
echo "firewalld installation rollback completed."
