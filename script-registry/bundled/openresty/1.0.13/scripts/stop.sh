#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(openresty_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "OpenResty is not installed."
emit_progress 10 service_stopping "正在停止 OpenResty"
stop_openresty
if openresty_is_running; then die "OpenResty is still active."; fi
emit_progress 100 service_stopped "OpenResty 已停止"
