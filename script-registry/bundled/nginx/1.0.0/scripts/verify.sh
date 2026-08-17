#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
emit_progress 10 validate_config "正在验证 Nginx 配置"
"${install_dir}/sbin/nginx" -t
verify_runtime_permissions
emit_progress 35 service_status "正在检查 Nginx 服务状态"
nginx_is_running || die "Nginx is not running."
emit_progress 55 verify_version "正在核对 Nginx 版本"
"${install_dir}/sbin/nginx" -v 2>&1 | grep -Fq "nginx/${software_version}"
emit_progress 75 health_check "正在执行 Nginx HTTP 健康检查"
curl --fail --silent --show-error --max-time 10 http://127.0.0.1/ >/dev/null
commit_external_nginx
emit_progress 90 finalize_state "正在确认 Nginx 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "Nginx 启动和健康检查通过"
echo "Nginx ${software_version} verification passed."
