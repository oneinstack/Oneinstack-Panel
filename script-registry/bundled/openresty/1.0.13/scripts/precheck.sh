#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
emit_progress 5 validate_inputs "正在校验 OpenResty 安装参数"
require_root; validate_inputs; validate_upgrade_path
emit_progress 35 check_host "正在检查操作系统兼容性"
check_host
if [[ "${install_mode}" == "offline" ]]; then
  validate_offline_bundle
fi
emit_progress 65 check_dependencies "正在检查安装依赖"
if [[ "${install_mode}" == "offline" ]]; then
  validate_offline_bundle
else
  require_command curl
fi
require_command tar; require_command awk; require_command df
case "${os_family}" in
  debian) require_command apt-get ;;
  rhel) command -v dnf >/dev/null 2>&1 || require_command yum ;;
  suse) require_command zypper ;;
esac
if command -v ss >/dev/null 2>&1; then
  listener="$(ss -H -ltnp "sport = :${openresty_port}" 2>/dev/null || true)"
else
  listener=""
  echo "Warning: ss is not installed; the post-dependency port check will run after package installation." >&2
fi
if [[ -n "${listener}" ]]; then
  if ! managed_openresty_owns_configured_port; then
    listener_owner="$(printf '%s\n' "${listener}" | sed -nE 's/.*users:\\(\\("([^"]+)".*/\\1/p' | head -n1)"
    [[ -n "${listener_owner}" ]] || listener_owner="another process"
    die "Port ${openresty_port} is already occupied by ${listener_owner}; choose a free port."
  fi
  emit_progress 70 port.managed.detected "端口 ${openresty_port} 正由当前受管 OpenResty 使用"
fi
emit_progress 80 check_disk "正在检查磁盘可用空间"
available_kb="$(df -Pk "$(dirname -- "${install_dir}")" | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 524288 ]] || die "At least 512 MiB free space is required under /usr/local."
emit_progress 100 precheck_completed "OpenResty 环境预检完成"
echo "OpenResty ${software_version} precheck passed."
