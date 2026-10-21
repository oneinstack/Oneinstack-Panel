#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/bin/redis-server" ]] || die "Redis is not installed."
load_runtime_probe_settings || die "Redis runtime probe settings are invalid."
verify_runtime_permissions
emit_progress 10 service_starting "正在启动 Redis"
systemctl start "${service_name}"
if [[ "$(redis_service_state)" == "active" ]]; then
  emit_progress 60 service_active "Redis 服务已启动，正在执行运行探针"
fi
if ! wait_for_redis_ready 180 0.5; then
  redis_start_failure_diagnostics
  die "Redis did not become ready after start."
fi
emit_progress 100 service_started "Redis 已启动"
