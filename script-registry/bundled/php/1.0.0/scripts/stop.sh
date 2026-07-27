#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" ]] || die "PHP-FPM is not installed."
emit_progress 10 service_stopping "正在停止 PHP-FPM"
systemctl stop php-fpm.service
if systemctl is-active --quiet php-fpm.service; then die "PHP-FPM is still active."; fi
emit_progress 100 service_stopped "PHP-FPM 已停止"
