#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -x "${nginx_binary}" ]] || die "Nginx is not installed."
emit_progress 10 service_restarting "正在重启 Nginx"
stop_nginx
start_nginx
nginx_is_running || die "Nginx did not become active."
emit_progress 100 service_restarted "Nginx 已重启"
