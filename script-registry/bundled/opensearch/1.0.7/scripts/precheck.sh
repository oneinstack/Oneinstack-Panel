#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
load_install_parameters
validate_inputs
validate_password
validate_kernel_settings
require_command systemctl
require_command ss
if [[ "${install_mode}" == offline ]]; then
  require_command sha256sum
  validate_offline_bundle
fi
if port_is_listening "${opensearch_port}" && ! service_is_active; then
  die "PORT_CONFLICT: OpenSearch target port ${opensearch_port} is already occupied by another process."
fi
if service_is_active || [[ -d "${install_dir}" || -d "${data_dir}" ]]; then
  emit_progress 30 existing_installation "检测到已有 OpenSearch；将保留数据，并在新实例通过验证前保留可回滚程序快照"
fi
if command -v dpkg-query >/dev/null 2>&1 && dpkg-query -W -f='${Status}' opensearch 2>/dev/null | grep -q 'install ok installed'; then
  emit_progress 40 external_package_detected "检测到发行包安装；Oneinstack 将接管 systemd 服务但保留原包以便回滚"
elif command -v rpm >/dev/null 2>&1 && rpm -q opensearch >/dev/null 2>&1; then
  emit_progress 40 external_package_detected "检测到发行包安装；Oneinstack 将接管 systemd 服务但保留原包以便回滚"
fi
emit_progress 100 precheck_completed "OpenSearch 安装前参数、主机矩阵和内核要求检查通过"
