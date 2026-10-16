#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
managed_installation_present || die_code COMPONENT_NOT_MANAGED "PHP-FPM is not managed by Oneinstack"
[[ -x "${install_dir}/sbin/php-fpm" ]] || die_code COMPONENT_NOT_MANAGED "Managed PHP-FPM binary is unavailable"
emit_progress 10 service_starting "正在启动 PHP-FPM"
start_managed_service
service_is_active || die_code SERVICE_START_FAILED "PHP-FPM did not become active"
wait_for_socket || die_code SERVICE_START_FAILED "PHP-FPM did not create the configured Unix socket"
emit_progress 100 service_started "PHP-FPM 已启动"
