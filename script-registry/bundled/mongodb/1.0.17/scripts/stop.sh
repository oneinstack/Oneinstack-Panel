#!/usr/bin/env bash
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; validate_inputs
managed_installation_present || die "Managed MongoDB installation is unavailable."
emit_progress 10 service_stopping "正在停止 MongoDB"
service_stop
service_is_active && die "MongoDB did not stop."
emit_progress 100 service_stopped "MongoDB 已停止"
