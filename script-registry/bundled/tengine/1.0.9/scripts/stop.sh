#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(tengine_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "Tengine is not installed."
emit_progress 10 service_stopping "正在停止 Tengine"
stop_tengine
if tengine_is_running; then die "Tengine is still active."; fi
emit_progress 100 service_stopped "Tengine 已停止"
