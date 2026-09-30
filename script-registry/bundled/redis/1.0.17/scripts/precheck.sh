#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 Redis 安装参数"
require_root
validate_inputs
check_base_commands
check_host
check_upgrade_direction
if [[ "${install_mode}" == "offline" ]]; then
  validate_offline_bundle
else
  require_command curl
fi
listener=""
if command -v ss >/dev/null 2>&1; then
  listener="$(ss -H -ltnp "sport = :${redis_port}" 2>/dev/null || true)"
else
  port_hex="$(printf '%04X' "${redis_port}")"
  proc_listener="$(awk -v port="${port_hex}" 'NR > 1 { split($2, endpoint, ":"); if (endpoint[2] == port && $4 == "0A") { print; exit } }' /proc/net/tcp /proc/net/tcp6 2>/dev/null || true)"
  [[ -z "${proc_listener}" ]] || die "Redis port ${redis_port} is occupied; ss is required to identify the listener."
fi
if [[ -n "${listener}" ]]; then
  [[ "${listener}" == *redis-server* || "${listener}" == *redis* ]] || die "Redis port ${redis_port} is occupied by an unrelated process."
  emit_progress 60 conflict.port.detected "Existing Redis listener detected and will be migrated"
fi
emit_progress 40 check_host "正在检查操作系统兼容性"
emit_progress 75 check_disk "正在检查 Redis 编译空间"
available_kb="$(df -Pk "$(dirname -- "${install_dir}")" | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 524288 ]] || die "At least 512 MiB free space is required."
if [[ "${install_mode}" == "offline" ]]; then
  source_artifact="${offline_package_path}/artifacts/${host_arch}/${source_archive}"
  [[ -f "${source_artifact}" ]] || die "Offline Redis source artifact is missing."
fi
emit_progress 100 precheck_completed "Redis 环境预检完成"
echo "Redis ${software_version} precheck passed."
