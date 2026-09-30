#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
[[ "${lifecycle_action}" == "install" || "${lifecycle_action}" == "upgrade" ]] ||
  die_code INVALID_ACTION "PHP verify script received an invalid lifecycle action"
emit_progress 10 validate_config "正在验证 PHP-FPM 配置"
"${install_dir}/sbin/php-fpm" --test --fpm-config "${install_dir}/etc/php-fpm.conf"
emit_progress 35 service_status "正在检查 PHP-FPM 服务状态"
service_is_active || die_code SERVICE_NOT_READY "PHP-FPM is not running"
emit_progress 55 verify_version "正在核对 PHP 版本"
actual_version="$("${install_dir}/bin/php" -r 'echo PHP_VERSION;' 2>/dev/null || true)"
[[ "${actual_version}" == "${software_version}" ]] ||
  die_code RUNTIME_VERSION_DRIFT "The installed PHP runtime version does not match the requested exact version"
emit_progress 75 health_check "正在检查 PHP-FPM Socket"
wait_for_socket || die_code SERVICE_NOT_READY "PHP-FPM Unix socket did not become ready"
verify_runtime_permissions
emit_progress 90 finalize_state "正在确认 PHP 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
mv -f -- "${state_dir}/pending-patch-version" "${state_dir}/patch-version"
mv -f -- "${state_dir}/pending-runtime-params" "${state_dir}/runtime-params"
: >"${state_dir}/managed"
emit_progress 100 verify_completed "PHP-FPM 启动和健康检查通过"
echo "component=php"
echo "version=${actual_version}"
echo "version_line=${actual_version%.*}.x"
echo "runtime_state=verified"
echo "socket_state=ready"
