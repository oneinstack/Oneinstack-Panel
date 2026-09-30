#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
managed_installation_present || die_code COMPONENT_NOT_MANAGED "PHP-FPM is not managed by Oneinstack"
[[ -x "${install_dir}/sbin/php-fpm" ]] || die_code COMPONENT_NOT_MANAGED "Managed PHP-FPM binary is unavailable"
emit_progress 10 service_restarting "正在重启 PHP-FPM"
stop_managed_service
start_managed_service
service_is_active || die_code SERVICE_START_FAILED "PHP-FPM did not become active"
wait_for_socket || die_code SERVICE_START_FAILED "PHP-FPM did not create the configured Unix socket"
emit_progress 100 service_restarted "PHP-FPM 已重启"
