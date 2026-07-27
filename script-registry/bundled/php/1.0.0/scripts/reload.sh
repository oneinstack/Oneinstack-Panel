#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/sbin/php-fpm" ]] || die "PHP-FPM is not installed."
systemctl is-active --quiet php-fpm.service || die "PHP-FPM is not running."
emit_progress 10 service_reloading "正在平滑重载 PHP-FPM"
"${install_dir}/sbin/php-fpm" --test
systemctl reload php-fpm.service
systemctl is-active --quiet php-fpm.service || die "PHP-FPM did not remain active."
emit_progress 100 service_reloaded "PHP-FPM 已平滑重载"
