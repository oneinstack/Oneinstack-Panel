#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154,SC2034
# shellcheck source=components/development/nodejs/scripts/common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
check_common_commands
validate_inputs
detect_host
select_artifact
if [[ "${install_mode}" == offline ]]; then
  artifact="$(offline_artifact_path)"
  verify_artifact
fi
verify_installed || die "${component_name} ${software_version} installation verification failed."
if [[ -f "${state_file}" ]]; then
  load_managed_install_dir
  [[ "${install_dir}" == "${INSTALL_DIR:-${install_dir}}" ]] ||
    die "Managed installation directory does not match INSTALL_DIR."
fi
emit_progress 100 verify_completed "${component_name} ${software_version} verification completed"
