#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(tengine_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "Tengine is not installed."
tengine_is_running || die "Tengine is not running."
emit_progress 10 service_reloading "正在平滑重载 Tengine"
"${runtime_binary}" -t -p "${install_dir}/" -c "$(tengine_main_config)"
reload_tengine
tengine_is_running || die "Tengine did not remain active."
emit_progress 100 service_reloaded "Tengine 已平滑重载"
