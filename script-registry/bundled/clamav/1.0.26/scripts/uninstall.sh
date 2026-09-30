#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source=common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
detect_host
if centos7_eol_runtime_profile; then
  # shellcheck source=container.sh
  source "${script_dir}/container.sh"
  container_uninstall
  exit 0
fi
is_managed || die 'NOT_MANAGED: refusing to remove an external ClamAV installation.'
load_managed_state
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
[[ "${data_policy}" == preserve || "${data_policy}" == delete ]] || die 'UNINSTALL_DATA_POLICY must be preserve or delete.'
if [[ "${data_policy}" == delete && "${UNINSTALL_CONFIRM_DATA_DELETION:-false}" != true ]]; then
  die 'UNINSTALL_CONFIRM_DATA_DELETION=true is required when deleting ClamAV data.'
fi
had_external=false
[[ -d "${external_dir}" ]] && had_external=true
remove_managed_units_and_config
remove_owned_packages
rollback_repository_bootstrap
report_preexisting_package_version_changes
restore_external_state
if [[ "${data_policy}" == delete && "${had_external}" != true ]]; then
  rm -rf -- "${data_dir}"
elif [[ "${data_policy}" == preserve && ! -d "${data_dir}" ]]; then
  # An external snapshot may legitimately have no database directory. Preserve
  # policy still guarantees the native package parent exists for a later APT
  # reinstall, without changing an existing external directory's metadata.
  install -d -m 0750 -- "${data_dir}"
fi
rm -rf -- "${state_dir}"
printf '%s uninstalled; data_policy=%s\n' "${component_name}" "${data_policy}"
