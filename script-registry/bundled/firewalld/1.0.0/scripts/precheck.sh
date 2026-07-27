#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

emit_progress 10 validate_inputs "正在校验 firewalld 安装参数"
require_root
validate_inputs
emit_progress 35 check_host "正在检查操作系统与包管理器"
check_host
emit_progress 65 check_disk "正在检查磁盘预留空间"
available_kb="$(df -Pk /var | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 131072 ]] ||
  die "At least 128 MiB free space is required under /var."
emit_progress 100 precheck_completed "firewalld 安装环境检查完成"
echo "firewalld installation precheck passed."
