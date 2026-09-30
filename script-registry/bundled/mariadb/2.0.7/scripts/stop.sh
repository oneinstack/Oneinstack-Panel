#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${config_file}" && -x "${install_dir}/bin/mariadbd" ]] || die "MariaDB is not installed."
emit_progress 10 service_stopping "正在停止 MariaDB"
service_stop
if service_is_active; then die "MariaDB is still active."; fi
emit_progress 100 service_stopped "MariaDB 已停止"
