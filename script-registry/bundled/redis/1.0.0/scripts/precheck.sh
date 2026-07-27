#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 Redis 安装参数"
require_root; validate_inputs
emit_progress 40 check_host "正在检查操作系统兼容性"
check_host
emit_progress 75 check_disk "正在检查 Redis 编译空间"
available_kb="$(df -Pk /usr/local | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 524288 ]] || die "At least 512 MiB free space is required."
emit_progress 100 precheck_completed "Redis 环境预检完成"
echo "Redis ${software_version} precheck passed."
