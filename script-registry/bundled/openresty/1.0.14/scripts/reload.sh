#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(openresty_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "OpenResty is not installed."
openresty_is_running || die "OpenResty is not running."
emit_progress 10 service_reloading "正在平滑重载 OpenResty"
"${runtime_binary}" -t -p "${install_dir}/nginx/" -c "$(openresty_main_config)"
reload_openresty
openresty_is_running || die "OpenResty did not remain active."
emit_progress 100 service_reloaded "OpenResty 已平滑重载"
