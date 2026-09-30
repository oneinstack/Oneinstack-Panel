#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mysqld" ]] || die "MySQL is not installed."
emit_progress 10 service_starting "正在启动 MySQL"
service_start
service_is_active || die "MySQL did not become active."
emit_progress 100 service_started "MySQL 已启动"
