#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
managed_installation_present || die_code COMPONENT_NOT_MANAGED "PHP-FPM is not managed by Oneinstack"
emit_progress 10 service_stopping "正在停止 PHP-FPM"
stop_managed_service
service_is_active && die_code SERVICE_STOP_FAILED "PHP-FPM is still active"
emit_progress 100 service_stopped "PHP-FPM 已停止"
