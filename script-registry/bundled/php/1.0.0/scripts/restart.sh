#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/sbin/php-fpm" ]] || die "PHP-FPM is not installed."
emit_progress 10 service_restarting "正在重启 PHP-FPM"
systemctl restart php-fpm.service
systemctl is-active --quiet php-fpm.service || die "PHP-FPM did not become active."
emit_progress 100 service_restarted "PHP-FPM 已重启"
