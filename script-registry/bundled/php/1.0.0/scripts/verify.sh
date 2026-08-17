#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
emit_progress 10 validate_config "正在验证 PHP-FPM 配置"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${install_dir}/etc/php-fpm.conf"
emit_progress 35 service_status "正在检查 PHP-FPM 服务状态"
systemctl is-active --quiet php-fpm
emit_progress 55 verify_version "正在核对 PHP 版本"
"${install_dir}/bin/php" -r 'exit(version_compare(PHP_VERSION, getenv("SOFTWARE_VERSION"), ">=") ? 0 : 1);'
emit_progress 75 health_check "正在检查 PHP-FPM Socket"
test -S /dev/shm/php-cgi.sock
verify_runtime_permissions
commit_external_php
emit_progress 90 finalize_state "正在确认 PHP 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
mv -f -- "${state_dir}/pending-patch-version" "${state_dir}/patch-version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "PHP-FPM 启动和健康检查通过"
echo "PHP ${patch_version} verification passed."
