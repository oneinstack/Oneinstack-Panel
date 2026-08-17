#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 MySQL 安装参数"
require_root; validate_inputs
emit_progress 35 check_host "正在检查操作系统兼容性"
check_host
emit_progress 70 check_disk "正在检查 MySQL 磁盘空间"
available_kb="$(df -Pk /usr/local | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 3145728 ]] || die "At least 3 GiB free space is required under /usr/local."
require_command ss
listener="$(ss -H -ltnp "sport = :${mysql_port}" 2>/dev/null || true)"
if [[ -n "${listener}" ]]; then
  [[ "${listener}" == *mysqld* ]] || die "MySQL port ${mysql_port} is occupied by an unrelated process."
  emit_progress 78 conflict.port.detected "Existing MySQL listener detected and will be migrated"
fi
emit_progress 85 check_database "正在检查 MySQL 数据目录"
if [[ ! -d "${data_dir}/mysql" && -z "${mysql_password}" ]]; then die "MYSQL_PASSWORD is required for a new database."; fi
emit_progress 100 precheck_completed "MySQL 环境预检完成"
echo "MySQL ${software_version} (${patch_version}) precheck passed."
