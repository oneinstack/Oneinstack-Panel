#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

emit_progress 5 validate_inputs "正在校验 MongoDB 安装参数"
require_root
validate_inputs
reject_immutable_runtime_changes
emit_progress 25 check_host "正在校验 MongoDB 主机平台、CPU 与内核"
detect_host
select_source
validate_upgrade_direction
[[ "${install_mode}" != offline ]] || validate_offline_bundle
has_systemd || die "INIT_SYSTEM_UNSUPPORTED: MongoDB managed lifecycle requires systemd."
if managed_installation_present; then
  current_version="$(<"${state_dir}/version")"
  if [[ "${current_version}" != "${software_version}" && "${ONEINSTACK_ACTION:-install}" != upgrade ]]; then
    die "UPGRADE_ACTION_REQUIRED: use the upgrade action to replace MongoDB ${current_version} with ${software_version}."
  fi
else
  [[ "${ONEINSTACK_ACTION:-install}" != upgrade ]] || die "UPGRADE_STATE_MISSING: no managed MongoDB installation can be upgraded."
fi
emit_progress 55 check_ownership "正在检查安装目录、数据目录与端口归属"
precheck_ownership
disk_path="$(dirname -- "${install_dir}")"
while [[ ! -e "${disk_path}" && "${disk_path}" != / ]]; do disk_path="$(dirname -- "${disk_path}")"; done
available_kb="$(df -Pk "${disk_path}" | awk 'NR==2 {print $4}')"
[[ "${available_kb}" =~ ^[0-9]+$ && "${available_kb}" -ge 2097152 ]] || die "DISK_SPACE_INSUFFICIENT: at least 2 GiB is required."
if ! is_loopback_bind "${mongodb_bind_ip}"; then
  printf 'SECURITY_NOTICE: MongoDB will listen on a non-loopback address with authentication enforced; restrict network access with a firewall.\n' >&2
fi
emit_progress 100 precheck_completed "MongoDB 环境预检完成"
