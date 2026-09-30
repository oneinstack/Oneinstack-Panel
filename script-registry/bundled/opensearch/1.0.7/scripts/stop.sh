#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root; load_install_parameters
managed_installation_present || die "Managed OpenSearch installation state is missing."
service_stop
if service_is_active; then
  die "OpenSearch service is still active after stop."
fi
emit_progress 100 stop_completed "OpenSearch 服务已停止，systemd 失败状态已清除"
