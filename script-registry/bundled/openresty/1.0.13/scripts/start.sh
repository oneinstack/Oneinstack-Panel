#!/usr/bin/env bash
# shellcheck source=common.sh
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
runtime_binary="$(openresty_runtime_binary || true)"
[[ -n "${runtime_binary}" ]] || die "OpenResty is not installed."
emit_progress 10 service_starting "正在启动 OpenResty"
start_openresty
openresty_is_running || die "OpenResty did not become active."
emit_progress 100 service_started "OpenResty 已启动"
