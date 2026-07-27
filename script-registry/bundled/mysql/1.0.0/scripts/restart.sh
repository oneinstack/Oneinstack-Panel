#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/bin/mysqld" ]] || die "MySQL is not installed."
emit_progress 10 service_restarting "正在重启 MySQL"
systemctl restart mysql.service
systemctl is-active --quiet mysql.service || die "MySQL did not become active."
emit_progress 100 service_restarted "MySQL 已重启"
