#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck disable=SC1091,SC2154,SC2034
# shellcheck source=components/development/nodejs/scripts/common.sh
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"

require_root
check_common_commands
[[ -f "${state_file}" ]] || die "Managed Node.js installation state is missing; refusing to remove unowned resources."
load_managed_install_dir
validate_managed_path "${install_dir}" INSTALL_DIR
load_persisted_conflict_backup
load_persisted_takeover_install_backup

remove_entrypoints
if [[ -f "${profile_file}" && ! -L "${profile_file}" ]] &&
  grep -Fq '# OneinStack managed Node.js PATH' "${profile_file}"; then
  rm -f -- "${profile_file}"
fi
restore_conflict_backups
[[ ! -e "${install_dir}" && ! -L "${install_dir}" ]] || rm -rf -- "${install_dir}"
restore_takeover_install_backup
[[ -z "${conflict_backup_dir}" || ! -e "${conflict_backup_dir}" ]] ||
  rm -rf -- "${conflict_backup_dir}"
rm -f -- "${state_file}"
rmdir "${state_dir}" 2>/dev/null || true
emit_progress 100 uninstall_completed "${component_name} managed runtime removed; previous unmanaged installation restored when present"
