#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
require_command getent
if [[ "${install_mode}" == "offline" ]]; then
  validate_offline_bundle
fi
check_external_firewall_conflict
emit_progress 10 precheck.host.validated "Host distribution and architecture are supported"

if [[ "${ONEINSTACK_ACTION:-install}" != "status" && "${ONEINSTACK_ACTION:-install}" != "configGet" ]]; then
  mkdir -p "${state_dir}"
  installed="$(runtime_version || true)"
  if firewalld_package_installed && [[ "${installed}" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] && runtime_version_matches_request "${installed}"; then
    emit_progress 45 precheck.package.reused "Using the installed firewalld package; repository download is not required"
  else
    refresh_package_index
    emit_progress 45 precheck.repository.ready "Official package repository is available"
    candidate="$(select_package_candidate)"
    printf '%s\n' "${candidate}" >"${state_dir}/candidate-version"
  fi
fi

if firewalld_package_installed && [[ -e /etc/firewalld ]] && ! firewalld_configuration_valid; then
  if ! service_active; then
    require_command firewall-offline-cmd
  fi
  die "CONFIG_INVALID" "Existing firewalld configuration is invalid; repair it before continuing."
fi
emit_progress 100 precheck.completed "firewalld precheck completed"
