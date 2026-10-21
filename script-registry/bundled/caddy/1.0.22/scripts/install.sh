#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
detect_platform
install -d -m 0750 -- "${state_dir}"
require_commands
systemd_available || die_code CADDY_SYSTEMD_REQUIRED "Caddy requires a running systemd service manager."
precheck_runtime_conflicts
emit_progress 5 resolve_artifact "Resolving the fixed official Caddy artifact"
artifact="$(resolve_artifact)"
emit_progress 18 snapshot "Creating the Caddy rollback snapshot"
prepare_rollback >/dev/null
rollback_install() {
  local code="$1"
  trap - ERR INT TERM
  warn "CADDY_INSTALL_ROLLBACK: installation failed; restoring the previous managed Caddy state."
  restore_rollback_snapshot || warn "CADDY_ROLLBACK_FAILED: Caddy rollback restoration failed."
  exit "${code}"
}
trap 'rollback_install $?' ERR
trap 'rollback_install 130' INT
trap 'rollback_install 143' TERM

stop_managed_caddy
port_listening && die_code CADDY_PORT_IN_USE "TCP port ${caddy_port} remains occupied after stopping the managed Caddy service."
emit_progress 30 prepare_directories "Preparing the Caddy account and managed directories"
prepare_directories
emit_progress 45 install_binary "Installing the verified Caddy ${software_version} binary"
install_binary_from_artifact "${artifact}"
ensure_low_port_capability
emit_progress 58 write_config "Writing Caddy-owned configuration and Panel vhost import"
render_managed_config "${managed_config}" "${caddy_port}" "${php_fpm_socket}" "${web_root}" "${log_dir}"
render_main_config "${caddyfile}"
emit_progress 70 validate_config "Validating Caddy configuration as caddy:caddy"
validate_caddy_config "${caddyfile}"
emit_progress 78 write_service "Installing oneinstack-caddy.service"
write_service_unit
if legacy_caddy_service_matches; then systemctl disable caddy.service >/dev/null 2>&1 || true; fi
emit_progress 86 start_service "Starting Caddy"
start_managed_caddy
emit_progress 92 verify_runtime "Verifying version, configuration, service, listener, and HTTP"
verify_runtime
persist_install_parameters
write_installed_state
report_phpmyadmin_integration
trap - ERR INT TERM
emit_progress 100 install_complete "Caddy ${software_version} installation completed from ${install_mode} source."
