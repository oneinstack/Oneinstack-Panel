#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mysqld" ]] || die "MySQL is not installed."
emit_progress 10 service_stopping "正在停止 MySQL"
service_stop
if service_is_active; then die "MySQL is still active."; fi
emit_progress 100 service_stopped "MySQL 已停止"
