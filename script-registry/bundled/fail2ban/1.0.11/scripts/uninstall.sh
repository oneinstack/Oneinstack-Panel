#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
[[ -f "${installed_marker}" && -f "${state_dir}/installed.json" ]] ||
  die "Managed Fail2ban state is missing; refusing to remove an external package."
emit_progress 20 stop_service "正在停止 Fail2ban 服务"
stop_redis_acl_collector
service_stop_disable
emit_progress 45 remove_managed_files "正在删除 OneinStack 管理的 Fail2ban 文件"
find /etc/fail2ban/jail.d -maxdepth 1 -type f -name '90-oneinstack-*.local' -delete 2>/dev/null || true
rm -f -- "${defaults_path}"
remove_managed_integration
if [[ -f "${installed_marker}" ]]; then
  emit_progress 70 uninstall_package "正在卸载由 OneinStack 安装的 Fail2ban 软件包"
  remove_package
fi
rm -f -- "${state_dir}/installed.json" "${installed_marker}"
rmdir "${state_dir}" 2>/dev/null || true
emit_progress 100 uninstall_completed "Fail2ban 组件卸载完成"
echo "Fail2ban component uninstalled; external Fail2ban configuration was preserved."
