#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 Nginx 安装参数"
require_root; validate_inputs; validate_install_version
emit_progress 35 check_host "正在检查操作系统兼容性"
check_host
emit_progress 65 check_dependencies "正在检查安装依赖"
require_command apt-get; require_command curl
emit_progress 80 check_disk "正在检查磁盘可用空间"
available_kb="$(df -Pk /usr/local | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 524288 ]] || die "At least 512 MiB free space is required under /usr/local."
emit_progress 100 precheck_completed "Nginx 环境预检完成"
echo "Nginx ${software_version} precheck passed."
