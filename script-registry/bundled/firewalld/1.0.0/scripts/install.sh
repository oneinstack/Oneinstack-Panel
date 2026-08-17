#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
install -d -m 0750 -- "${state_dir}"
emit_progress 12 prepare_install "正在检查 firewalld 软件包与系统配置"

if [[ -f "${installed_marker}" ]] && firewalld_configuration_valid; then
  emit_progress 100 already_installed "firewalld managed installation is healthy"
  exit 0
fi

if command -v firewall-cmd >/dev/null 2>&1 &&
  command -v firewall-offline-cmd >/dev/null 2>&1; then
  emit_progress 20 conflict.software.detected "Existing firewalld installation detected; migrating configuration"
fi

snapshot_existing
if ! firewalld_package_installed; then
  emit_progress 35 install.package.installing "正在通过系统包管理器安装 firewalld"
  install_firewalld_package
else
  ensure_firewalld_stopped
  emit_progress 35 migration.package.reinstalling "Reinstalling firewalld under component management"
  reinstall_firewalld_package
fi
: >"${installed_marker}"

emit_progress 75 verify_protocols "正在验证 IPsec 协议数据库"
ensure_protocol_database
emit_progress 85 keep_disabled "正在保持 firewalld 关闭，等待面板端口保护"
ensure_firewalld_stopped
emit_progress 100 install_completed "firewalld 软件包安装完成"
echo "firewalld installed and left disabled until the Panel protects its management port."
