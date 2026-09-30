#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
if [[ "${install_mode}" == "offline" ]]; then
  validate_offline_bundle
  install_dependencies_offline
fi
check_external_firewall_conflict
mkdir -p "${state_dir}"
snapshot_existing
trap 'restore_existing' ERR
installed="$(runtime_version || true)"
if firewalld_package_installed && [[ "${installed}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] && runtime_version_matches_request "${installed}"; then
  emit_progress 35 package.current "The requested firewalld runtime is already installed"
else
  refresh_package_index
  candidate="$(select_package_candidate)"
  emit_progress 35 package.candidate.selected "Selected an exact firewalld upgrade candidate"
  install_firewalld_package "${candidate}"
fi
firewalld_package_installed || die "SCRIPT_EXECUTION_FAILED" "firewalld upgrade did not install the package."
ensure_panel_port_protected
write_default_managed_rules
firewalld_configuration_valid || die "CONFIG_INVALID" "firewalld configuration validation failed after upgrade."
if [[ -r "${migration_dir}/external-backend" ]]; then
  migrate_external_firewall_backend
  ensure_service_started
elif [[ "${ONEINSTACK_WAS_ACTIVE:-false}" == "true" || -f "${migration_dir}/was-active" ]]; then
  if systemd_available; then
    systemctl reload firewalld.service || systemctl restart firewalld.service || die "SERVICE_RELOAD_FAILED" "firewalld could not be reloaded or restarted after upgrade."
    service_active || die "SERVICE_RELOAD_FAILED" "firewalld is not active after upgrade."
  else
    stop_firewalld_without_systemd
    start_firewalld_without_systemd
  fi
else
  ensure_service_stopped
fi
trap - ERR
printf '%s\n' "${requested_version}" >"${state_dir}/pending-version"
emit_progress 100 upgrade.completed "firewalld upgraded with the existing configuration preserved"
