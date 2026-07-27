#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
install -d -m 0750 -- "${state_dir}"
emit_progress 12 prepare_install "正在检查 firewalld 软件包与系统配置"

if command -v firewall-cmd >/dev/null 2>&1 &&
  command -v firewall-offline-cmd >/dev/null 2>&1; then
  emit_progress 20 repair_dependencies "正在检查 firewalld 系统协议数据库"
  ensure_protocol_database
  if firewalld_configuration_valid; then
    emit_progress 100 already_installed "firewalld 已安装且配置完整"
    echo "firewalld is already installed and its offline configuration is valid."
    exit 0
  fi
  emit_progress 30 repair_package "正在修复 firewalld 软件包与系统配置"
fi

was_installed=0
firewalld_package_installed && was_installed=1
if ! firewalld_package_installed; then
  emit_progress 30 install_package "正在通过系统包管理器安装 firewalld"
fi
install_firewalld_package
if [[ "${was_installed}" -eq 0 ]]; then
  : >"${installed_marker}"
fi

emit_progress 75 verify_protocols "正在验证 IPsec 协议数据库"
ensure_protocol_database
emit_progress 85 keep_disabled "正在保持 firewalld 关闭，等待面板端口保护"
ensure_firewalld_stopped
emit_progress 100 install_completed "firewalld 软件包安装完成"
echo "firewalld installed and left disabled until the Panel protects its management port."
