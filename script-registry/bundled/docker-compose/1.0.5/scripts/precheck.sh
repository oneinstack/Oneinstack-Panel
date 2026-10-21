#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
validate_inputs
check_host
check_engine
require_command curl
require_command sha256sum
if [[ "${install_mode}" == "offline" ]]; then
  [[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline component manifest is missing."
  [[ -f "${offline_package_path}/artifacts/${host_arch}/${artifact_name}" ]] ||
    die "Offline Docker Compose artifact is missing for ${host_arch}."
  [[ -d "${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}" ]] ||
    die "Offline host dependency directory is missing for ${system_id} ${system_version} ${host_arch}."
  resolve_artifact
fi
if [[ -f "${plugin_path}" ]] || compose_version >/dev/null 2>&1; then
  emit_progress 60 migration.software.detected "Existing Docker Compose installation detected; managed adoption will be used"
fi
emit_progress 100 precheck.completed "Docker Engine 29.8.0 and Docker Compose fixed artifact inputs are available"
