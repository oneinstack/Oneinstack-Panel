#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 PHP 安装参数"
require_root; validate_inputs
emit_progress 40 check_host "正在检查操作系统兼容性"
check_host
emit_progress 75 check_disk "正在检查 PHP 编译空间"
available_kb="$(df -Pk /usr/local | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 1048576 ]] || die "At least 1 GiB free space is required."
emit_progress 100 precheck_completed "PHP 环境预检完成"
echo "PHP ${software_version} (${patch_version}) precheck passed."
