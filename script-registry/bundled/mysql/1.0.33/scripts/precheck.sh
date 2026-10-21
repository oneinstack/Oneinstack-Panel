#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 MySQL 安装参数"
require_root; validate_inputs
reject_immutable_runtime_changes
emit_progress 35 check_host "正在检查操作系统兼容性"
check_host
if [[ "${install_mode}" == "offline" ]]; then validate_offline_bundle; fi
emit_progress 70 check_disk "正在检查 MySQL 磁盘空间"
available_kb="$(df -Pk "$(dirname -- "${install_dir}")" | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 3145728 ]] || die "At least 3 GiB free space is required for the MySQL installation."
if [[ "${ONEINSTACK_ACTION:-install}" == "upgrade" ]]; then
  [[ -f "${state_dir}/version" ]] || die "Cannot upgrade MySQL because the managed installation state is missing."
  [[ -d "${data_dir}/mysql" ]] || die "Cannot upgrade MySQL because the managed data directory is missing."
elif managed_installation_present; then
  emit_progress 78 managed.installation.detected "检测到已有受管 MySQL，按恢复流程继续"
else
listener=""
if command -v ss >/dev/null 2>&1; then
  listener="$(ss -H -ltnp "sport = :${mysql_port}" 2>/dev/null || true)"
else
  port_hex="$(printf '%04X' "${mysql_port}")"
  proc_listener="$(awk -v port="${port_hex}" 'NR > 1 { split($2, endpoint, ":"); if (endpoint[2] == port && $4 == "0A") { print; exit } }' /proc/net/tcp /proc/net/tcp6 2>/dev/null || true)"
  [[ -z "${proc_listener}" ]] || die "MySQL port ${mysql_port} is occupied and the listener cannot be safely identified without ss."
fi
if [[ -n "${listener}" ]]; then
  if [[ "${listener}" != *mysqld* ]]; then
    die "MySQL port ${mysql_port} is occupied by an unrelated process."
  fi
  [[ "${MIGRATE_EXTERNAL_MYSQL:-false}" == true && "${MIGRATE_EXTERNAL_CONFIRM:-false}" == true ]] ||
    die "An external MySQL listener is using the requested port; migration requires both explicit confirmation parameters."
  emit_progress 78 conflict.port.detected "检测到外部 MySQL 监听，将按显式确认执行受控接管"
fi
if [[ ! -f "${state_dir}/version" && -d "${install_dir}" ]] &&
  find "${install_dir}" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null | grep -q .; then
  die "The target install directory contains unmanaged files; refusing to overwrite unknown resources."
fi
emit_progress 85 check_database "正在检查 MySQL 数据目录"
if [[ ! -d "${data_dir}/mysql" && -z "${mysql_password}" ]]; then die "MYSQL_PASSWORD is required for a new database."; fi
if external_mysql_detected && [[ "${MIGRATE_EXTERNAL_MYSQL:-false}" != true || "${MIGRATE_EXTERNAL_CONFIRM:-false}" != true ]]; then
  die "An external MySQL-compatible instance was detected; migration requires both explicit confirmation parameters."
fi
fi
verify_existing_database_password
emit_progress 100 precheck_completed "MySQL 环境预检完成"
echo "MySQL ${software_version} (${patch_version}) precheck passed."
