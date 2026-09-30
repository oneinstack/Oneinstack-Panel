#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 PHP 安装参数"
require_root; validate_inputs
emit_progress 40 check_host "正在检查操作系统兼容性"
check_host
[[ "${install_mode}" != "offline" ]] || validate_offline_bundle
emit_progress 55 check_external "正在检查 PHP-FPM 归属和迁移条件"
validate_external_policy
if [[ "${lifecycle_action}" == "upgrade" ]] && ! managed_installation_present; then
  die_code COMPONENT_NOT_MANAGED "Upgrade requires an existing Oneinstack-managed PHP installation"
fi
emit_progress 75 check_disk "正在检查 PHP 编译空间"
available_kb="$(df -Pk "$(dirname -- "${install_dir}")" | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 1048576 ]] || die "At least 1 GiB free space is required."
emit_progress 100 precheck_completed "PHP 环境预检完成"
echo "component=php"
echo "version=${software_version}"
echo "version_line=${software_version%.*}.x"
echo "action=${lifecycle_action}"
echo "systemd=$(systemd_available && echo true || echo false)"
echo "precheck=passed"
