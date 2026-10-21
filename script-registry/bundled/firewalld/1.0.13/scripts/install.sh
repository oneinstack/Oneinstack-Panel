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
installed="$(runtime_version || true)"
if firewalld_package_installed && firewalld_configuration_valid && [[ "${installed}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] && runtime_version_matches_request "${installed}"; then
  if [[ -f "${installed_marker}" ]]; then
    external_backend="$(external_firewall_backend || true)"
    if [[ "${external_backend}" == "iptables" ]]; then
      service_active && die "FIREWALL_BACKEND_CONFLICT" "The managed firewalld service is active while an external iptables backend is present."
      write_default_managed_rules
      ensure_service_disabled
      emit_progress 100 install.already.current "The requested firewalld version is already installed; iptables was retained and firewalld remains stopped"
    elif [[ -n "${external_backend}" ]]; then
      takeover_external_firewall
      emit_progress 100 install.already.current "firewalld was already installed and took over from the external firewall backend"
    else
      ensure_panel_port_protected
      write_default_managed_rules
      ensure_service_started
      emit_progress 100 install.already.current "The requested firewalld version is already installed, enabled, and active"
    fi
  else
    snapshot_existing
    trap 'restore_existing' ERR
    ensure_panel_port_protected
    write_default_managed_rules
    if [[ -r "${migration_dir}/external-backend" ]]; then
      migrate_external_firewall_backend
      ensure_service_started
      activation_result="migrated"
    elif [[ "$(external_firewall_backend || true)" == "iptables" ]]; then
      ensure_service_disabled
      activation_result="retained-iptables"
    else
      ensure_service_started
      activation_result="started"
    fi
    trap - ERR
    commit_state
    case "${activation_result}" in
      migrated) emit_progress 100 install.adopted_existing "The installed firewalld package was adopted and took over from the external firewall backend" ;;
      retained-iptables) emit_progress 100 install.adopted_existing "The installed firewalld package was adopted; external iptables rules were retained and firewalld remains disabled" ;;
      *) emit_progress 100 install.adopted_existing "The installed firewalld package was adopted, enabled, and started" ;;
    esac
  fi
  exit 0
fi

snapshot_existing
trap 'restore_existing' ERR
refresh_package_index
candidate="$(select_package_candidate)"
emit_progress 40 package.candidate.selected "Selected an exact firewalld package candidate"
install_firewalld_package "${candidate}"
firewalld_package_installed || die "SCRIPT_EXECUTION_FAILED" "firewalld package installation did not complete."
ensure_panel_port_protected
write_default_managed_rules
if [[ -r "${migration_dir}/external-backend" ]]; then
  migrate_external_firewall_backend
  ensure_service_started
  activation_result="migrated"
elif [[ "$(external_firewall_backend || true)" == "iptables" ]]; then
  ensure_service_disabled
  activation_result="retained-iptables"
else
  ensure_service_started
  activation_result="started"
fi
trap - ERR
printf '%s\n' "${requested_version}" >"${state_dir}/pending-version"
case "${activation_result}" in
  migrated) emit_progress 100 install.completed "firewalld installed and took over from the external firewall backend" ;;
  retained-iptables) emit_progress 100 install.completed "firewalld package installed; external iptables rules were retained and firewalld remains disabled" ;;
  *) emit_progress 100 install.completed "firewalld installed, enabled, and started after Panel port protection" ;;
esac
