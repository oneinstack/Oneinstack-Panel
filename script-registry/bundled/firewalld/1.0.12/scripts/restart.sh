#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
require_command firewall-cmd
check_external_firewall_conflict
external_backend="$(external_firewall_backend || true)"
if [[ -n "${external_backend}" ]]; then
  takeover_external_firewall
  emit_progress 100 service.restarted "firewalld restarted after taking over the external firewall backend"
  exit 0
fi
firewalld_configuration_valid || die "CONFIG_INVALID" "firewalld configuration validation failed."
ensure_panel_port_protected
if systemd_available; then
  systemctl restart firewalld.service
else
  stop_firewalld_without_systemd
  start_firewalld_without_systemd
fi
service_active || die "SERVICE_START_FAILED" "firewalld did not become active after restart."
firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_START_FAILED" "firewalld command interface is not ready after restart."
emit_progress 100 service.restarted "firewalld restarted after Panel port protection"
