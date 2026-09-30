#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_service_action restart
  exit 0
fi
is_managed || die 'NOT_MANAGED: ClamAV is not managed by Oneinstack.'
load_managed_state
systemctl restart "${service_name}.service"
wait_for_healthy || { health_failure_detail; die 'SERVICE_NOT_READY: ClamAV did not become healthy after restart.'; }
