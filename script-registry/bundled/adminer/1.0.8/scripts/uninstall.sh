#!/usr/bin/env bash
set -Eeuo pipefail
# shellcheck source-path=SCRIPTDIR
source "$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/common.sh"
require_root
data_policy="${UNINSTALL_DATA_POLICY:-preserve}"
delete_confirm="${UNINSTALL_CONFIRM_DATA_DELETION:-false}"
case "${data_policy}" in preserve|delete) ;; *) die_code ADMINER_UNINSTALL_POLICY_INVALID "data-policy must be preserve or delete." ;; esac
[[ "${delete_confirm}" == true || "${delete_confirm}" == false ]] || die_code ADMINER_UNINSTALL_CONFIRM_INVALID "delete-data-confirm must be true or false."
[[ -f "${installed_state}" || -f "${state_dir}/installed" ]] || die_code ADMINER_STATE_MISSING "Managed installation state is missing; refusing to remove unowned files."
load_runtime_state
validate_public_path "${public_path}"
web_document_root="$(read_parameter "${runtime_state}" 'document-root')"
validate_path "${web_document_root}" WEB_SERVER_DOCUMENT_ROOT
route_path="${web_document_root}/${public_path#/}"
route_path="${route_path%/}"
remove_managed_public_route "${route_path}" 2>/dev/null || true
if [[ "${data_policy}" == delete ]]; then
  [[ "${delete_confirm}" == true ]] || die_code ADMINER_DELETE_CONFIRM_REQUIRED "data-policy=delete requires delete-data-confirm=true."
  rm -rf -- "${install_dir}"
else
  rm -f -- "${install_dir}/index.php" "${install_dir}/${artifact_name}" "${install_dir}/adminer-plugins.php" "${install_dir}/oneinstack-config.php"
  rmdir "${install_dir}" 2>/dev/null || true
fi
remove_managed_php_path_acl_entries "${managed_php_path_acl_file}"
rm -rf -- "${state_dir}"
if [[ "${data_policy}" == delete ]]; then
  printf 'Adminer uninstalled; managed directory deleted: %s\n' "${install_dir}"
else
  printf 'Adminer uninstalled; generated files removed and user plugin files preserved at %s\n' "${install_dir}"
fi
emit_progress 100 uninstall_completed "Adminer public route and managed files removed"
