#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_precheck
  exit 0
fi
validate_inputs
validate_mandatory_access_prerequisites
emit_install_profile
if [[ "${install_mode}" == offline ]]; then
  validate_offline_bundle
else
  validate_online_repositories
fi
available_kib="$(df -Pk "$(dirname -- "${data_dir}")" | awk 'NR == 2 {print $4}')"
[[ "${available_kib}" =~ ^[0-9]+$ && "${available_kib}" -ge 1048576 ]] ||
  die 'DISK_SPACE_INSUFFICIENT: at least 1 GiB is required for ClamAV packages and signature databases.'
if ! is_managed && find_existing >/dev/null; then
  snapshot_existing
  emit_progress 30 existing_installation '检测到原生 ClamAV，已创建可回滚迁移快照'
fi
emit_progress 100 precheck_completed "${component_name} ${system_id} ${system_version} ${host_arch} prerequisites passed"
