#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "$0")/common.sh"
require_root
validate_inputs
[[ -f "${unit_file}" ]] || die "Redis is not installed."
emit_progress 10 service_stopping "正在停止 Redis"
systemctl stop redis.service
if systemctl is-active --quiet redis.service; then die "Redis is still active."; fi
emit_progress 100 service_stopped "Redis 已停止"
