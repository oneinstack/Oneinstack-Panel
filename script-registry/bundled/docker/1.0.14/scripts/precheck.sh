#!/usr/bin/env bash
# shellcheck disable=SC1091,SC2154
set -Eeuo pipefail
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
validate_inputs
check_host
check_host_prerequisites
if command -v docker >/dev/null 2>&1; then
  emit_progress 40 conflict.software.detected "Existing Docker installation detected; migration replacement will be used"
fi
if [[ "${install_mode}" == "offline" ]]; then
  require_command sha256sum
  [[ -n "${offline_package_path}" ]] || die "Offline installation requires ONEINSTACK_OFFLINE_PACKAGE_PATH."
  [[ -f "${offline_package_path}/manifest.yaml" ]] || die "Offline component manifest is missing."
  [[ -f "${offline_package_path}/artifacts/${host_arch}/${artifact_name}" ]] ||
    die "Offline Docker Engine artifact is missing for ${host_arch}."
  [[ -d "${offline_package_path}/packages/${system_id}/${system_version}/${host_arch}" ]] ||
    die "Offline host dependency directory is missing for ${system_id} ${system_version} ${host_arch}."
  resolve_artifact
fi
emit_progress 100 precheck_completed "Docker host prerequisites and fixed artifact inputs are available"
