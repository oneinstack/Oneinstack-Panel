#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
managed_installation_present || die_code COMPONENT_NOT_MANAGED "PHP-FPM is not managed by Oneinstack"
[[ -x "${install_dir}/sbin/php-fpm" ]] || die_code COMPONENT_NOT_MANAGED "Managed PHP-FPM binary is unavailable"
service_is_active || die_code SERVICE_NOT_READY "PHP-FPM is not running"
emit_progress 10 service_reloading "正在平滑重载 PHP-FPM"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${fpm_config_file}"
if systemd_available && managed_unit_matches; then
  systemctl reload "${service_name}.service"
else
  pid="$(cat -- "${pid_file}")"
  pid_is_managed "${pid}" || die_code SERVICE_RELOAD_FAILED "Managed PHP-FPM process identity is unavailable"
  kill -USR2 "${pid}"
fi
service_is_active || die_code SERVICE_RELOAD_FAILED "PHP-FPM did not remain active after reload"
wait_for_socket || die_code SERVICE_RELOAD_FAILED "PHP-FPM socket did not remain ready after reload"
emit_progress 100 service_reloaded "PHP-FPM 已平滑重载"
