#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_rollback
  exit 0
fi
rollback_installation
rm -rf -- "${state_dir}"
printf '%s rollback completed\n' "${component_name}"
