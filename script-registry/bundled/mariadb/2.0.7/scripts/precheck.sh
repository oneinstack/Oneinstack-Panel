#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

emit_progress 5 validate_inputs "正在校验 MariaDB 安装参数"
require_root
validate_inputs
reject_immutable_runtime_changes

emit_progress 25 check_host "正在检查 MariaDB 操作系统兼容性"
check_host
if [[ "${install_mode}" == offline ]]; then
  validate_offline_bundle
fi

emit_progress 45 check_disk "正在检查 MariaDB 构建和数据磁盘空间"
build_available_kb="$(df -Pk /usr/local | awk 'NR == 2 {print $4}')"
if is_centos7_rpm_runtime; then
  required_build_kb=1048576
  disk_requirement="At least 1 GiB free space is required under /usr/local for the MariaDB CentOS 7 runtime."
else
  required_build_kb=10485760
  disk_requirement="At least 10 GiB free space is required under /usr/local/src to build MariaDB."
fi
[[ "${build_available_kb}" =~ ^[0-9]+$ && "${build_available_kb}" -ge "${required_build_kb}" ]] ||
  die "${disk_requirement}"
data_parent="${data_dir}"
while [[ ! -e "${data_parent}" ]]; do
  next_parent="$(dirname -- "${data_parent}")"
  [[ "${next_parent}" != "${data_parent}" ]] || break
  data_parent="${next_parent}"
done
data_available_kb="$(df -Pk "${data_parent}" | awk 'NR == 2 {print $4}')"
[[ "${data_available_kb}" =~ ^[0-9]+$ && "${data_available_kb}" -ge 1048576 ]] ||
  die "At least 1 GiB free space is required for the MariaDB data directory."

validate_managed_upgrade_path
if [[ "${ONEINSTACK_ACTION:-install}" == upgrade ]]; then
  managed_installation_present ||
    die "Cannot upgrade MariaDB because managed installation state or runtime files are missing."
elif managed_installation_present; then
  emit_progress 62 managed_installation_detected "检测到已有受管 MariaDB，将按同版本线修复流程继续"
elif preserved_managed_data_present &&
  [[ ! -d /var/lib/mysql ]] &&
  ! external_mysql_runtime_detected; then
  validate_preserved_managed_data
  emit_progress 62 preserved_data_detected "检测到卸载时保留的受管 MariaDB 数据，将继续恢复安装"
else
  external_mysql_detected &&
    die "An external MySQL-compatible server or data directory was detected; MariaDB refuses automatic takeover or migration."
  if [[ -d "${install_dir}" ]] &&
    find "${install_dir}" -mindepth 1 -maxdepth 1 -print -quit 2>/dev/null | grep -q .; then
    die "The MariaDB install directory contains unmanaged files."
  fi
  if command -v ss >/dev/null 2>&1 &&
    ss -H -ltn "sport = :${mysql_port}" 2>/dev/null | grep -q .; then
    die "MariaDB port ${mysql_port} is already occupied."
  fi
  [[ ! -d "${data_dir}/mysql" ]] || preserved_managed_data_present ||
    die "The MariaDB data directory is initialized but has no trusted Oneinstack ownership state."
fi

if [[ -d "${data_dir}/mysql" ]]; then
  [[ -n "${mysql_password}" ]] ||
    die "MYSQL_PASSWORD is required to recover or verify the existing MariaDB data directory."
else
  [[ -n "${mysql_password}" ]] ||
    die "MYSQL_PASSWORD is required for a new MariaDB database."
fi
verify_existing_database_password
emit_progress 100 precheck_completed "MariaDB 环境预检完成"
printf 'MariaDB %s precheck passed.\n' "${software_version}"
