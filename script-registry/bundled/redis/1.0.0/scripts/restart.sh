#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/bin/redis-server" ]] || die "Redis is not installed."
emit_progress 10 service_restarting "正在重启 Redis"
systemctl restart redis.service
systemctl is-active --quiet redis.service || die "Redis did not become active."
emit_progress 100 service_restarted "Redis 已重启"
