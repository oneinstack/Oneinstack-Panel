#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
[[ -f "${installed_marker}" ]] || die "PACKAGE_UNAVAILABLE" "firewalld is not managed by OneinStack."
ensure_service_disabled
remove_firewalld_package || die "SCRIPT_EXECUTION_FAILED" "The managed firewalld package could not be removed."

policy="${UNINSTALL_DATA_POLICY:-preserve}"
confirm="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
case "${policy}" in
  preserve)
    rm -f -- "${installed_marker}"
    printf '%s\n' "uninstalled" >"${state_dir}/state"
    emit_progress 100 uninstall.preserved "firewalld configuration, logs, rules, and recovery data were preserved"
    ;;
  delete)
    [[ "${confirm}" == "true" ]] || die "CONFIG_INVALID" "Data deletion requires explicit confirmation."
    rm -f -- "${installed_marker}" "${managed_rules_file}"
    rm -f -- "${state_dir}/candidate-version" "${state_dir}/pending-version" "${state_dir}/requested-version" "${state_dir}/runtime-version" "${state_dir}/package-version" "${state_dir}/state"
    rm -rf -- "${migration_dir}"
    rmdir "${state_dir}" 2>/dev/null || true
    emit_progress 100 uninstall.deleted "Only OneinStack firewalld state was deleted"
    ;;
  *) die "CONFIG_INVALID" "UNINSTALL_DATA_POLICY must be preserve or delete." ;;
esac
