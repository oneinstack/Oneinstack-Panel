#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
emit_progress 15 service_status "正在检查 Redis 服务状态"
systemctl is-active --quiet redis || die "Redis service is not active."
export REDISCLI_AUTH="${redis_password}"
emit_progress 45 health_check "正在执行 Redis PING 健康检查"
[[ "$("${install_dir}/bin/redis-cli" -h 127.0.0.1 -p "${redis_port}" ping 2>/dev/null || true)" == "PONG" ]] || die "Redis PING health check failed."
emit_progress 70 verify_version "正在核对 Redis 版本"
"${install_dir}/bin/redis-server" --version | grep -Fq "v=${software_version}" || die "Redis runtime version does not match ${software_version}."
emit_progress 90 finalize_state "正在确认 Redis 安装状态"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "Redis 启动和健康检查通过"
echo "Redis ${software_version} verification passed."
