#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(tengine_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "Tengine is not installed."
emit_progress 10 service_restarting "正在重启 Tengine"
stop_tengine
start_tengine
tengine_is_running || die "Tengine did not become active."
emit_progress 100 service_restarted "Tengine 已重启"
