#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
require_command firewall-cmd
require_command getent
if ! service_active; then
  require_command firewall-offline-cmd
fi
firewalld_package_installed || die "PACKAGE_UNAVAILABLE" "The firewalld package is not installed."
actual="$(runtime_version || true)"
[[ -n "${actual}" ]] || die "SCRIPT_EXECUTION_FAILED" "The installed firewalld runtime version could not be read."
runtime_version_matches_request "${actual}" || die "VERSION_MISMATCH" "Installed firewalld version ${actual} does not match requested ${requested_version}."
getent protocols esp >/dev/null 2>&1 || die "HOST_DEPENDENCY_UNAVAILABLE" "The IPsec ESP protocol database entry is unavailable."
firewalld_configuration_valid || die "CONFIG_INVALID" "firewalld configuration validation failed."
expect_active="${ONEINSTACK_EXPECT_ACTIVE:-false}"
if [[ "${ONEINSTACK_ACTION:-}" == "install" && "$(external_firewall_backend || true)" != "iptables" ]]; then
  expect_active="true"
fi
if [[ "${expect_active}" == "true" ]]; then
  service_active || die "SERVICE_START_FAILED" "firewalld is not active after the requested lifecycle action."
  firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld command interface is not ready."
elif service_active; then
  firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld is active but its command interface is not ready."
fi
commit_state
emit_progress 100 verify.completed "firewalld package, configuration, runtime, and service checks passed"
