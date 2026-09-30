#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
managed_installation_present || die "Managed MongoDB installation is unavailable."
emit_progress 10 service_starting "正在启动 MongoDB"
service_start
wait_for_mongodb "$(config_scalar net port "${mongodb_port}")" "$(config_scalar net bindIp "${mongodb_bind_ip}")"
emit_progress 100 service_started "MongoDB 已启动"
