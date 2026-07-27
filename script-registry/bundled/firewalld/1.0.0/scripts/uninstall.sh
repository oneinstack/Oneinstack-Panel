#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
  die "Disable firewalld from the Security page before uninstalling it."
fi
emit_progress 20 uninstall_package "正在卸载 firewalld 软件包"
if firewalld_package_installed; then
  remove_firewalld_package
fi
rm -rf -- "${state_dir}"
emit_progress 100 uninstall_completed "firewalld 已卸载"
echo "firewalld uninstalled."
