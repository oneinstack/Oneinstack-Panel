#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154,SC2034
# shellcheck source=components/development/nodejs/scripts/common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
check_common_commands
validate_inputs
[[ -f "${state_file}" ]] || {
  emit_progress 100 rollback_completed "${component_name} failed transaction was already restored"
  exit 0
}

load_managed_install_dir
validate_managed_path "${install_dir}" INSTALL_DIR

if [[ "${ONEINSTACK_ACTION:-install}" != install ]]; then
  emit_progress 100 rollback_completed "${component_name} upgrade transaction was already restored before this action"
  exit 0
fi

load_persisted_conflict_backup
load_persisted_takeover_install_backup
remove_entrypoints
if is_managed_profile; then
  rm -f -- "${profile_file}"
fi
restore_conflict_backups
[[ ! -e "${install_dir}" && ! -L "${install_dir}" ]] || rm -rf -- "${install_dir}"
restore_takeover_install_backup
[[ -z "${conflict_backup_dir}" || ! -e "${conflict_backup_dir}" ]] ||
  rm -rf -- "${conflict_backup_dir}"
rm -f -- "${state_file}"
rmdir "${state_dir}" 2>/dev/null || true
emit_progress 100 rollback_completed "${component_name} installation rollback restored the previous unmanaged state"
