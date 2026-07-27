#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" ]] || die "MySQL is not installed."
emit_progress 10 service_stopping "正在停止 MySQL"
systemctl stop mysql.service
if systemctl is-active --quiet mysql.service; then die "MySQL is still active."; fi
emit_progress 100 service_stopped "MySQL 已停止"
