#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
if [[ -d "${migration_dir}" ]]; then
  restore_existing || die "ROLLBACK_FAILED" "The previous firewalld package or configuration could not be restored."
  exit 0
fi
if [[ -f "${installed_marker}" ]] && firewalld_package_installed; then
  ensure_service_disabled
  remove_firewalld_package || die "ROLLBACK_FAILED" "The newly installed firewalld package could not be removed."
  rm -f -- "${installed_marker}" "${managed_rules_file}"
  emit_progress 100 rollback.package.removed "New firewalld package removed"
  exit 0
fi
die "RECOVERY_REQUIRED" "No firewalld rollback snapshot is available."
