#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -x "${nginx_binary}" ]] || die "Nginx is not installed."
emit_progress 10 service_stopping "正在停止 Nginx"
stop_nginx
if nginx_is_running; then die "Nginx is still active."; fi
emit_progress 100 service_stopped "Nginx 已停止"
