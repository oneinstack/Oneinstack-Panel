#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mariadbd" ]] || die "MariaDB is not installed."
emit_progress 10 service_restarting "正在重启 MariaDB"
service_restart
service_is_active || die "MariaDB did not become active."
emit_progress 100 service_restarted "MariaDB 已重启"
