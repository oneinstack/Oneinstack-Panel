#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
validate_inputs
verify_runtime_permissions
load_runtime_probe_settings || die "Redis runtime probe settings are invalid."
emit_progress 15 service_status "正在检查 Redis 服务状态"
emit_progress 45 health_check "正在执行 Redis PING 健康检查"
if ! wait_for_redis_ready 180 0.5; then
  service_state="$(redis_service_state)"
  [[ "${service_state}" == "active" ]] || die "Redis service is not active: ${service_state:-unknown}."
  die "Redis runtime PING probe failed."
fi
emit_progress 70 verify_version "正在核对 Redis 版本"
"${install_dir}/bin/redis-server" --version | grep -Fq "v=${software_version}" || die "Redis runtime version does not match ${software_version}."
commit_external_redis
emit_progress 90 finalize_state "正在确认 Redis 安装状态"
install -d -m 0750 -- "${state_dir}"
mv -f -- "${state_dir}/pending-version" "${state_dir}/version"
if [[ -f "${state_dir}/pending-source-sha256" ]]; then mv -f -- "${state_dir}/pending-source-sha256" "${state_dir}/source-sha256"; fi
if [[ -f "${state_dir}/pending-install-mode" ]]; then mv -f -- "${state_dir}/pending-install-mode" "${state_dir}/install-mode"; fi
printf '%s\n' "${install_dir}" >"${state_dir}/install-dir"
printf '%s\n' "${data_dir}" >"${state_dir}/data-dir"
rm -rf -- "${rollback_dir}"
emit_progress 100 verify_completed "Redis 启动和健康检查通过"
echo "Redis ${software_version} verification passed."
