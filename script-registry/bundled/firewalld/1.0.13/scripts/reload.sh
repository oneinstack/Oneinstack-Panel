#!/usr/bin/env bash
# shellcheck disable=SC2154
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154
source "$(dirname -- "${BASH_SOURCE[0]}")/common.sh"

require_root
validate_inputs
check_host
require_command firewall-cmd
firewalld_configuration_valid || die "CONFIG_INVALID" "firewalld configuration validation failed."
if service_active; then
  firewall-cmd --reload || die "SERVICE_RELOAD_FAILED" "firewalld reload failed."
  firewall-cmd --state >/dev/null 2>&1 || die "SERVICE_RELOAD_FAILED" "firewalld is not ready after reload."
fi
emit_progress 100 service.reloaded "firewalld configuration reload completed"
