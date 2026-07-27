#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

validate_inputs
emit_progress 20 verify_commands "正在检查 firewalld 管理命令"
require_command firewall-cmd
require_command firewall-offline-cmd
require_command python3
require_command getent
emit_progress 65 verify_version "正在读取 firewalld 版本"
# firewall-cmd connects to the system D-Bus even for --version. That creates a
# false negative in containers and on hosts where firewalld is intentionally
# kept stopped until the Panel has protected its own management port.
python3 -c 'from firewall.config import VERSION; print(VERSION)'
emit_progress 75 verify_protocols "正在校验系统协议数据库与 firewalld 配置"
getent protocols esp >/dev/null ||
  die "The system protocol database is missing the IPsec ESP protocol."
firewall-offline-cmd --check-config
emit_progress 85 verify_disabled "正在确认防火墙等待安全启用"
if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet firewalld 2>/dev/null; then
  die "firewalld unexpectedly started before the Panel port was protected."
fi
emit_progress 100 verify_completed "firewalld 安装验证完成，可在安全页启用"
echo "firewalld installation verified."
