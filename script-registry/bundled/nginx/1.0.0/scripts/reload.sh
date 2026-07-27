#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -x "${nginx_binary}" ]] || die "Nginx is not installed."
nginx_is_running || die "Nginx is not running."
emit_progress 10 service_reloading "正在平滑重载 Nginx"
"${install_dir}/sbin/nginx" -t
reload_nginx
nginx_is_running || die "Nginx did not remain active."
emit_progress 100 service_reloaded "Nginx 已平滑重载"
