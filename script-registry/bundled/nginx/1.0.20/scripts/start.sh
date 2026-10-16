#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -x "${nginx_binary}" ]] || die "Nginx is not installed."
emit_progress 10 service_starting "正在启动 Nginx"
start_nginx
nginx_is_running || die "Nginx did not become active."
emit_progress 100 service_started "Nginx 已启动"
