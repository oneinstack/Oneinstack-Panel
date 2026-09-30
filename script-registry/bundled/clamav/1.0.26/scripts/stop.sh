#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_service_action stop
  exit 0
fi
is_managed || die 'NOT_MANAGED: ClamAV is not managed by Oneinstack.'
systemctl stop "${service_name}.service"
