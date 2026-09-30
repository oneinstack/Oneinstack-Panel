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
[[ "${install_mode}" == offline ]] || require_command curl
if [[ "${install_mode}" == offline ]]; then
  artifact="$(offline_artifact_path)"
  verify_artifact
fi

parent_dir="$(dirname -- "${install_dir}")"
[[ -d "${parent_dir}" ]] || die "Node.js installation parent directory does not exist: ${parent_dir}."

if [[ "${route}" == source ]]; then
  require_command "$(package_manager)"
  check_build_resources
  dependency_dir="$(offline_dependency_dir)"
  if [[ "${install_mode}" == offline ]]; then
    [[ -d "${dependency_dir}" ]] ||
      die "Offline dependency bundle is missing for ${system_id} ${system_version} ${architecture}."
    compgen -G "${dependency_dir}/*.rpm" >/dev/null ||
      die "Offline CentOS 7 build dependency bundle is empty."
  fi
fi

if [[ -e "${install_dir}" || -L "${install_dir}" ]]; then
  if [[ -f "${state_file}" ]]; then
    load_managed_install_dir
    validate_managed_path "${install_dir}" INSTALL_DIR
  else
    [[ "${takeover_unmanaged_conflicts}" == true ]] ||
      die "Refusing to replace an unmanaged Node.js installation. Set TAKEOVER_UNMANAGED_CONFLICTS=true only after approving a backup."
  fi
fi

check_entrypoint_conflicts

emit_progress 10 precheck_completed "${component_name} ${software_version} precheck completed for ${system_id} ${system_version} ${architecture} ${route} route"
