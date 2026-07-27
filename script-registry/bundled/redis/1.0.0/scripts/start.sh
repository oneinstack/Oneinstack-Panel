#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" && -x "${install_dir}/bin/redis-server" ]] || die "Redis is not installed."
emit_progress 10 service_starting "正在启动 Redis"
systemctl start redis.service
systemctl is-active --quiet redis.service || die "Redis did not become active."
emit_progress 100 service_started "Redis 已启动"
